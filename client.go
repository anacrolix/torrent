/*
Package torrent implements a torrent client.

Simple example:

	c := &Client{}
	c.Start()
	defer c.Stop()
	if err := c.AddTorrent(externalMetaInfoPackageSux); err != nil {
		return fmt.Errors("error adding torrent: %s", err)
	}
	c.WaitAll()
	log.Print("erhmahgerd, torrent downloaded")

*/
package torrent

import (
	"bufio"
	"bytes"
	"container/heap"
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log"
	"math/big"
	mathRand "math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/h2so5/utp"

	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/libtorgo/metainfo"

	"bitbucket.org/anacrolix/go.torrent/dht"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
	. "bitbucket.org/anacrolix/go.torrent/util"
	"bitbucket.org/anacrolix/go.torrent/util/levelmu"
)

var (
	unusedDownloadedChunksCount = expvar.NewInt("unusedDownloadedChunksCount")
	chunksDownloadedCount       = expvar.NewInt("chunksDownloadedCount")
	peersFoundByDHT             = expvar.NewInt("peersFoundByDHT")
	peersFoundByPEX             = expvar.NewInt("peersFoundByPEX")
	uploadChunksPosted          = expvar.NewInt("uploadChunksPosted")
	unexpectedCancels           = expvar.NewInt("unexpectedCancels")
	postedCancels               = expvar.NewInt("postedCancels")
	duplicateConnsAvoided       = expvar.NewInt("duplicateConnsAvoided")
	failedPieceHashes           = expvar.NewInt("failedPieceHashes")
	unsuccessfulDials           = expvar.NewInt("unsuccessfulDials")
	successfulDials             = expvar.NewInt("successfulDials")
	acceptedConns               = expvar.NewInt("acceptedConns")
)

const (
	// Justification for set bits follows.
	//
	// Extension protocol: http://www.bittorrent.org/beps/bep_0010.html
	// DHT: http://www.bittorrent.org/beps/bep_0005.html
	extensionBytes = "\x00\x00\x00\x00\x00\x10\x00\x01"

	socketsPerTorrent = 40
)

// Currently doesn't really queue, but should in the future.
func (cl *Client) queuePieceCheck(t *torrent, pieceIndex pp.Integer) {
	piece := t.Pieces[pieceIndex]
	if piece.QueuedForHash {
		return
	}
	piece.QueuedForHash = true
	go cl.verifyPiece(t, pieceIndex)
}

func (cl *Client) queueFirstHash(t *torrent, piece int) {
	p := t.Pieces[piece]
	if p.EverHashed || p.Hashing || p.QueuedForHash {
		return
	}
	cl.queuePieceCheck(t, pp.Integer(piece))
}

// Queues the torrent data for the given region for download. The beginning of
// the region is given highest priority to allow a subsequent read at the same
// offset to return data ASAP.
func (me *Client) PrioritizeDataRegion(ih InfoHash, off, len_ int64) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	t := me.torrent(ih)
	if t == nil {
		return errors.New("no such active torrent")
	}
	if !t.haveInfo() {
		return errors.New("missing metadata")
	}
	me.downloadStrategy.TorrentPrioritize(t, off, len_)
	for _, cn := range t.Conns {
		me.replenishConnRequests(t, cn)
	}
	me.openNewConns(t)
	return nil
}

type dataWait struct {
	offset int64
	ready  chan struct{}
}

type Client struct {
	noUpload         bool
	dataDir          string
	halfOpenLimit    int
	peerID           [20]byte
	listeners        []net.Listener
	disableTrackers  bool
	downloadStrategy DownloadStrategy
	dHT              *dht.Server
	disableUTP       bool

	mu    levelmu.LevelMutex
	event sync.Cond
	quit  chan struct{}

	handshaking int

	torrents map[InfoHash]*torrent

	dataWaits map[*torrent][]dataWait
}

func (me *Client) PeerID() string {
	return string(me.peerID[:])
}

func (me *Client) ListenAddr() (addr net.Addr) {
	for _, l := range me.listeners {
		if addr != nil && l.Addr().String() != addr.String() {
			panic("listeners exist on different addresses")
		}
		addr = l.Addr()
	}
	return
}

type hashSorter struct {
	Hashes []InfoHash
}

func (me hashSorter) Len() int {
	return len(me.Hashes)
}

func (me hashSorter) Less(a, b int) bool {
	return (&big.Int{}).SetBytes(me.Hashes[a][:]).Cmp((&big.Int{}).SetBytes(me.Hashes[b][:])) < 0
}

func (me hashSorter) Swap(a, b int) {
	me.Hashes[a], me.Hashes[b] = me.Hashes[b], me.Hashes[a]
}

func (cl *Client) sortedTorrents() (ret []*torrent) {
	var hs hashSorter
	for ih := range cl.torrents {
		hs.Hashes = append(hs.Hashes, ih)
	}
	sort.Sort(hs)
	for _, ih := range hs.Hashes {
		ret = append(ret, cl.torrent(ih))
	}
	return
}

func (cl *Client) WriteStatus(_w io.Writer) {
	cl.mu.LevelLock(1)
	defer cl.mu.Unlock()
	w := bufio.NewWriter(_w)
	defer w.Flush()
	fmt.Fprintf(w, "Listening on %s\n", cl.ListenAddr())
	fmt.Fprintf(w, "Peer ID: %q\n", cl.peerID)
	fmt.Fprintf(w, "Handshaking: %d\n", cl.handshaking)
	if cl.dHT != nil {
		fmt.Fprintf(w, "DHT nodes: %d\n", cl.dHT.NumNodes())
		fmt.Fprintf(w, "DHT Server ID: %x\n", cl.dHT.IDString())
		fmt.Fprintf(w, "DHT port: %d\n", addrPort(cl.dHT.LocalAddr()))
		fmt.Fprintf(w, "DHT announces: %d\n", cl.dHT.NumConfirmedAnnounces)
	}
	cl.downloadStrategy.WriteStatus(w)
	fmt.Fprintln(w)
	for _, t := range cl.sortedTorrents() {
		if t.Name() == "" {
			fmt.Fprint(w, "<unknown name>")
		} else {
			fmt.Fprint(w, t.Name())
		}
		if t.haveInfo() {
			fmt.Fprintf(w, ": %f%% of %d bytes", 100*(1-float32(t.BytesLeft())/float32(t.Length())), t.Length())
		}
		fmt.Fprint(w, "\n")
		fmt.Fprint(w, "Blocked reads:")
		for _, dw := range cl.dataWaits[t] {
			fmt.Fprintf(w, " %d", dw.offset)
		}
		fmt.Fprintln(w)
		t.WriteStatus(w)
		fmt.Fprintln(w)
	}
}

// Read torrent data at the given offset. Returns ErrDataNotReady if the data
// isn't available.
func (cl *Client) TorrentReadAt(ih InfoHash, off int64, p []byte) (n int, err error) {
	cl.mu.LevelLock(1)
	defer cl.mu.Unlock()
	t := cl.torrent(ih)
	if t == nil {
		err = errors.New("unknown torrent")
		return
	}
	index := pp.Integer(off / int64(t.UsualPieceSize()))
	// Reading outside the bounds of a file is an error.
	if index < 0 {
		err = os.ErrInvalid
		return
	}
	if int(index) >= len(t.Pieces) {
		err = io.EOF
		return
	}
	piece := t.Pieces[index]
	pieceOff := pp.Integer(off % int64(t.UsualPieceSize()))
	pieceLeft := int(t.PieceLength(index) - pieceOff)
	if pieceLeft <= 0 {
		err = io.EOF
		return
	}
	if len(p) > pieceLeft {
		p = p[:pieceLeft]
	}
	for cs, _ := range piece.PendingChunkSpecs {
		chunkOff := int64(pieceOff) - int64(cs.Begin)
		if chunkOff >= int64(t.PieceLength(index)) {
			panic(chunkOff)
		}
		if 0 <= chunkOff && chunkOff < int64(cs.Length) {
			// read begins in a pending chunk
			err = ErrDataNotReady
			return
		}
		// pending chunk caps available data
		if chunkOff < 0 && int64(len(p)) > -chunkOff {
			p = p[:-chunkOff]
		}
	}
	if len(p) == 0 {
		panic(len(p))
	}
	return t.Data.ReadAt(p, off)
}

func NewClient(cfg *Config) (cl *Client, err error) {
	if cfg == nil {
		cfg = &Config{}
	}

	cl = &Client{
		noUpload:         cfg.NoUpload,
		disableTrackers:  cfg.DisableTrackers,
		downloadStrategy: cfg.DownloadStrategy,
		halfOpenLimit:    socketsPerTorrent,
		dataDir:          cfg.DataDir,
		disableUTP:       cfg.DisableUTP,

		quit:     make(chan struct{}),
		torrents: make(map[InfoHash]*torrent),

		dataWaits: make(map[*torrent][]dataWait),
	}
	cl.event.L = &cl.mu
	cl.mu.Init(2)

	if cfg.PeerID != "" {
		CopyExact(&cl.peerID, cfg.PeerID)
	} else {
		o := copy(cl.peerID[:], BEP20)
		_, err = rand.Read(cl.peerID[o:])
		if err != nil {
			panic("error generating peer id")
		}
	}

	if cl.downloadStrategy == nil {
		cl.downloadStrategy = &DefaultDownloadStrategy{}
	}

	// Returns the laddr string to listen on for the next Listen call.
	listenAddr := func() string {
		if addr := cl.ListenAddr(); addr != nil {
			return addr.String()
		}
		if cfg.ListenAddr == "" {
			return ":50007"
		}
		return cfg.ListenAddr
	}
	if !cfg.DisableTCP {
		var l net.Listener
		l, err = net.Listen("tcp", listenAddr())
		if err != nil {
			return
		}
		cl.listeners = append(cl.listeners, l)
		go cl.acceptConnections(l, false)
	}
	var utpL *utp.UTPListener
	if !cfg.DisableUTP {
		utpL, err = utp.Listen("utp", listenAddr())
		if err != nil {
			return
		}
		cl.listeners = append(cl.listeners, utpL)
		go cl.acceptConnections(utpL, true)
	}
	if !cfg.NoDHT {
		cfg := dht.ServerConfig{
			Addr: listenAddr(),
		}
		if utpL != nil {
			cfg.Conn = utpL.RawConn
		}
		cl.dHT, err = dht.NewServer(&cfg)
		if err != nil {
			return
		}
	}

	return
}

func (cl *Client) stopped() bool {
	select {
	case <-cl.quit:
		return true
	default:
		return false
	}
}

// Stops the client. All connections to peers are closed and all activity will
// come to a halt.
func (me *Client) Stop() {
	me.mu.Lock()
	close(me.quit)
	for _, l := range me.listeners {
		l.Close()
	}
	me.event.Broadcast()
	for _, t := range me.torrents {
		t.Close()
	}
	me.mu.Unlock()
}

func (cl *Client) acceptConnections(l net.Listener, utp bool) {
	for {
		// We accept all connections immediately, because we don't what
		// torrent they're for.
		conn, err := l.Accept()
		select {
		case <-cl.quit:
			if conn != nil {
				conn.Close()
			}
			return
		default:
		}
		if err != nil {
			log.Print(err)
			return
		}
		acceptedConns.Add(1)
		go func() {
			if err := cl.runConnection(conn, nil, peerSourceIncoming, utp); err != nil {
				log.Print(err)
			}
		}()
	}
}

func (me *Client) torrent(ih InfoHash) *torrent {
	for _, t := range me.torrents {
		if t.InfoHash == ih {
			return t
		}
	}
	return nil
}

type dialResult struct {
	net.Conn
	UTP bool
}

func doDial(dial func() (net.Conn, error), ch chan dialResult, utp bool) {
	conn, err := dial()
	if err != nil {
		conn = nil // Pedantic
	}
	ch <- dialResult{conn, utp}
	if err == nil {
		successfulDials.Add(1)
		return
	}
	unsuccessfulDials.Add(1)
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return
	}
	if netOpErr, ok := err.(*net.OpError); ok {
		switch netOpErr.Err {
		case syscall.ECONNREFUSED, syscall.EHOSTUNREACH:
			return
		}
	}
	if err != nil {
		log.Printf("error connecting to peer: %s %#v", err, err)
		return
	}
}

func reducedDialTimeout(max time.Duration, halfOpenLimit int, pendingPeers int) (ret time.Duration) {
	ret = max / time.Duration((pendingPeers+halfOpenLimit)/halfOpenLimit)
	if ret < minDialTimeout {
		ret = minDialTimeout
	}
	return
}

// Start the process of connecting to the given peer for the given torrent if
// appropriate.
func (me *Client) initiateConn(peer Peer, t *torrent) {
	if peer.Id == me.peerID {
		return
	}
	addr := net.JoinHostPort(peer.IP.String(), fmt.Sprintf("%d", peer.Port))
	if t.addrActive(addr) {
		duplicateConnsAvoided.Add(1)
		return
	}
	dialTimeout := reducedDialTimeout(nominalDialTimeout, me.halfOpenLimit, len(t.Peers))
	t.HalfOpen[addr] = struct{}{}
	go func() {
		// Binding to the listen address and dialing via net.Dialer gives
		// "address in use" error. It seems it's not possible to dial out from
		// this address so that peers associate our local address with our
		// listen address.

		// Initiate connections via TCP and UTP simultaneously. Use the first
		// one that succeeds.
		left := 1
		if !me.disableUTP {
			left++
		}
		resCh := make(chan dialResult, left)
		if !me.disableUTP {
			go doDial(func() (net.Conn, error) {
				return (&utp.Dialer{Timeout: dialTimeout}).Dial("utp", addr)
			}, resCh, true)
		}
		go doDial(func() (net.Conn, error) {
			// time.Sleep(time.Second) // Give uTP a bit of a head start.
			return net.DialTimeout("tcp", addr, dialTimeout)
		}, resCh, false)
		var res dialResult
		for ; left > 0 && res.Conn == nil; left-- {
			res = <-resCh
		}
		// Whether or not the connection attempt succeeds, the half open
		// counter should be decremented, and new connection attempts made.
		go func() {
			me.mu.Lock()
			defer me.mu.Unlock()
			if _, ok := t.HalfOpen[addr]; !ok {
				panic("invariant broken")
			}
			delete(t.HalfOpen, addr)
			me.openNewConns(t)
		}()
		if res.Conn == nil {
			return
		}
		if left > 0 {
			go func() {
				for ; left > 0; left-- {
					conn := (<-resCh).Conn
					if conn != nil {
						conn.Close()
					}
				}
			}()
		}

		// log.Printf("connected to %s", conn.RemoteAddr())
		err := me.runConnection(res.Conn, t, peer.Source, res.UTP)
		if err != nil {
			log.Print(err)
		}
	}()
}

// The port number for incoming peer connections. 0 if the client isn't
// listening.
func (cl *Client) incomingPeerPort() int {
	listenAddr := cl.ListenAddr()
	if listenAddr == nil {
		return 0
	}
	return addrPort(listenAddr)
}

// Convert a net.Addr to its compact IP representation. Either 4 or 16 bytes
// per "yourip" field of http://www.bittorrent.org/beps/bep_0010.html.
func addrCompactIP(addr net.Addr) (string, error) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if v4 := ip.To4(); v4 != nil {
		if len(v4) != 4 {
			panic(v4)
		}
		return string(v4), nil
	}
	return string(ip.To16()), nil
}

func handshakeWriter(w io.WriteCloser, bb <-chan []byte, done chan<- error) {
	var err error
	for b := range bb {
		_, err = w.Write(b)
		if err != nil {
			w.Close()
			break
		}
	}
	done <- err
}

type peerExtensionBytes [8]byte
type peerID [20]byte

type handshakeResult struct {
	peerExtensionBytes
	peerID
	InfoHash
}

func handshake(sock io.ReadWriteCloser, ih *InfoHash, peerID [20]byte) (res handshakeResult, ok bool, err error) {
	// Bytes to be sent to the peer. Should never block the sender.
	postCh := make(chan []byte, 4)
	// A single error value sent when the writer completes.
	writeDone := make(chan error, 1)
	// Performs writes to the socket and ensures posts don't block.
	go handshakeWriter(sock, postCh, writeDone)

	defer func() {
		close(postCh) // Done writing.
		if !ok {
			return
		}
		if err != nil {
			panic(err)
		}
		// Wait until writes complete before returning from handshake.
		err = <-writeDone
		if err != nil {
			err = fmt.Errorf("error writing during handshake: %s", err)
		}
	}()

	post := func(bb []byte) {
		select {
		case postCh <- bb:
		default:
			panic("mustn't block while posting")
		}
	}

	post([]byte(pp.Protocol))
	post([]byte(extensionBytes))
	if ih != nil { // We already know what we want.
		post(ih[:])
		post(peerID[:])
	}
	var b [68]byte
	_, err = io.ReadFull(sock, b[:68])
	if err != nil {
		err = nil
		return
	}
	if string(b[:20]) != pp.Protocol {
		return
	}
	CopyExact(&res.peerExtensionBytes, b[20:28])
	CopyExact(&res.InfoHash, b[28:48])
	CopyExact(&res.peerID, b[48:68])

	if ih == nil { // We were waiting for the peer to tell us what they wanted.
		post(res.InfoHash[:])
		post(peerID[:])
	}

	ok = true
	return
}

type peerConn struct {
	net.Conn
}

func (pc peerConn) Read(b []byte) (n int, err error) {
	// Keep-alives should be received every 2 mins. Give a bit of gracetime.
	err = pc.Conn.SetReadDeadline(time.Now().Add(150 * time.Second))
	if err != nil {
		return
	}
	n, err = pc.Conn.Read(b)
	if err != nil {
		if opError, ok := err.(*net.OpError); ok && opError.Op == "read" && opError.Err == syscall.ECONNRESET {
			err = io.EOF
		} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			if n != 0 {
				panic(n)
			}
			err = io.EOF
		}
	}
	return
}

func (me *Client) runConnection(sock net.Conn, torrent *torrent, discovery peerSource, uTP bool) (err error) {
	if tcpConn, ok := sock.(*net.TCPConn); ok {
		tcpConn.SetLinger(0)
	}
	defer sock.Close()
	me.mu.Lock()
	me.handshaking++
	me.mu.Unlock()
	// One minute to complete handshake.
	sock.SetDeadline(time.Now().Add(time.Minute))
	hsRes, ok, err := handshake(sock, func() *InfoHash {
		if torrent == nil {
			return nil
		} else {
			return &torrent.InfoHash
		}
	}(), me.peerID)
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.handshaking == 0 {
		panic("handshake count invariant is broken")
	}
	me.handshaking--
	if err != nil {
		err = fmt.Errorf("error during handshake: %s", err)
		return
	}
	if !ok {
		return
	}
	if hsRes.peerID == me.peerID {
		return
	}
	torrent = me.torrent(hsRes.InfoHash)
	if torrent == nil {
		return
	}
	sock.SetWriteDeadline(time.Time{})
	sock = peerConn{sock}
	conn := newConnection(sock, hsRes.peerExtensionBytes, hsRes.peerID, uTP)
	defer conn.Close()
	conn.Discovery = discovery
	if !me.addConnection(torrent, conn) {
		return
	}
	if conn.PeerExtensionBytes[5]&0x10 != 0 {
		conn.Post(pp.Message{
			Type:       pp.Extended,
			ExtendedID: pp.HandshakeExtendedID,
			ExtendedPayload: func() []byte {
				d := map[string]interface{}{
					"m": map[string]int{
						"ut_metadata": 1,
						"ut_pex":      2,
					},
					"v": "go.torrent dev 20140825", // Just the date
					// No upload queue is implemented yet.
					"reqq": func() int {
						if me.noUpload {
							// No need to look strange if it costs us nothing.
							return 250
						} else {
							return 1
						}
					}(),
				}
				if torrent.metadataSizeKnown() {
					d["metadata_size"] = torrent.metadataSize()
				}
				if p := me.incomingPeerPort(); p != 0 {
					d["p"] = p
				}
				yourip, err := addrCompactIP(conn.Socket.RemoteAddr())
				if err != nil {
					log.Printf("error calculating yourip field value in extension handshake: %s", err)
				} else {
					d["yourip"] = yourip
				}
				// log.Printf("sending %v", d)
				b, err := bencode.Marshal(d)
				if err != nil {
					panic(err)
				}
				return b
			}(),
		})
	}
	if torrent.haveAnyPieces() {
		conn.Post(pp.Message{
			Type:     pp.Bitfield,
			Bitfield: torrent.bitfield(),
		})
	}
	if conn.PeerExtensionBytes[7]&0x01 != 0 && me.dHT != nil {
		addr, _ := me.dHT.LocalAddr().(*net.UDPAddr)
		conn.Post(pp.Message{
			Type: pp.Port,
			Port: uint16(addr.Port),
		})
	}
	err = me.connectionLoop(torrent, conn)
	if err != nil {
		err = fmt.Errorf("during Connection loop with peer %q: %s", conn.PeerID, err)
	}
	me.dropConnection(torrent, conn)
	return
}

func (me *Client) peerGotPiece(t *torrent, c *connection, piece int) {
	for piece >= len(c.PeerPieces) {
		c.PeerPieces = append(c.PeerPieces, false)
	}
	c.PeerPieces[piece] = true
	if !t.havePiece(piece) {
		me.replenishConnRequests(t, c)
	}
}

func (me *Client) peerUnchoked(torrent *torrent, conn *connection) {
	me.replenishConnRequests(torrent, conn)
}

func (cl *Client) connCancel(t *torrent, cn *connection, r request) (ok bool) {
	ok = cn.Cancel(r)
	if ok {
		postedCancels.Add(1)
		cl.downloadStrategy.DeleteRequest(t, r)
	}
	return
}

func (cl *Client) connDeleteRequest(t *torrent, cn *connection, r request) {
	if !cn.RequestPending(r) {
		return
	}
	cl.downloadStrategy.DeleteRequest(t, r)
	delete(cn.Requests, r)
}

func (cl *Client) requestPendingMetadata(t *torrent, c *connection) {
	if t.haveInfo() {
		return
	}
	var pending []int
	for index := 0; index < t.MetadataPieceCount(); index++ {
		if !t.HaveMetadataPiece(index) {
			pending = append(pending, index)
		}
	}
	for _, i := range mathRand.Perm(len(pending)) {
		c.Post(pp.Message{
			Type:       pp.Extended,
			ExtendedID: byte(c.PeerExtensionIDs["ut_metadata"]),
			ExtendedPayload: func() []byte {
				b, err := bencode.Marshal(map[string]int{
					"msg_type": 0,
					"piece":    pending[i],
				})
				if err != nil {
					panic(err)
				}
				return b
			}(),
		})
	}
}

func (cl *Client) completedMetadata(t *torrent) {
	h := sha1.New()
	h.Write(t.MetaData)
	var ih InfoHash
	CopyExact(&ih, h.Sum(nil))
	if ih != t.InfoHash {
		log.Print("bad metadata")
		t.InvalidateMetadata()
		return
	}
	var info metainfo.Info
	err := bencode.Unmarshal(t.MetaData, &info)
	if err != nil {
		log.Printf("error unmarshalling metadata: %s", err)
		t.InvalidateMetadata()
		return
	}
	// TODO(anacrolix): If this fails, I think something harsher should be
	// done.
	err = cl.setMetaData(t, info, t.MetaData)
	if err != nil {
		log.Printf("error setting metadata: %s", err)
		t.InvalidateMetadata()
		return
	}
	log.Printf("%s: got metadata from peers", t)
}

// Process incoming ut_metadata message.
func (cl *Client) gotMetadataExtensionMsg(payload []byte, t *torrent, c *connection) (err error) {
	var d map[string]int
	err = bencode.Unmarshal(payload, &d)
	if err != nil {
		err = fmt.Errorf("error unmarshalling payload: %s: %q", err, payload)
		return
	}
	msgType, ok := d["msg_type"]
	if !ok {
		err = errors.New("missing msg_type field")
		return
	}
	piece := d["piece"]
	switch msgType {
	case pp.DataMetadataExtensionMsgType:
		if t.haveInfo() {
			break
		}
		begin := len(payload) - metadataPieceSize(d["total_size"], piece)
		if begin < 0 || begin >= len(payload) {
			log.Printf("got bad metadata piece")
			break
		}
		t.SaveMetadataPiece(piece, payload[begin:])
		if !t.HaveAllMetadataPieces() {
			break
		}
		cl.completedMetadata(t)
	case pp.RequestMetadataExtensionMsgType:
		if !t.HaveMetadataPiece(piece) {
			c.Post(t.NewMetadataExtensionMessage(c, pp.RejectMetadataExtensionMsgType, d["piece"], nil))
			break
		}
		start := (1 << 14) * piece
		c.Post(t.NewMetadataExtensionMessage(c, pp.DataMetadataExtensionMsgType, piece, t.MetaData[start:start+t.metadataPieceSize(piece)]))
	case pp.RejectMetadataExtensionMsgType:
	default:
		err = errors.New("unknown msg_type value")
	}
	return
}

type peerExchangeMessage struct {
	Added      CompactPeers   `bencode:"added"`
	AddedFlags []byte         `bencode:"added.f"`
	Dropped    []tracker.Peer `bencode:"dropped"`
}

// Extracts the port as an integer from an address string.
func addrPort(addr net.Addr) int {
	return AddrPort(addr)
}

// Processes incoming bittorrent messages. The client lock is held upon entry
// and exit.
func (me *Client) connectionLoop(t *torrent, c *connection) error {
	decoder := pp.Decoder{
		R:         bufio.NewReaderSize(c.Socket, 20*1024),
		MaxLength: 256 * 1024,
	}
	for {
		me.mu.Unlock()
		var msg pp.Message
		err := decoder.Decode(&msg)
		me.mu.Lock()
		c.lastMessageReceived = time.Now()
		select {
		case <-c.closing:
			return nil
		default:
		}
		if err != nil {
			if me.stopped() || err == io.EOF {
				return nil
			}
			return err
		}
		if msg.Keepalive {
			continue
		}
		switch msg.Type {
		case pp.Choke:
			c.PeerChoked = true
			for r := range c.Requests {
				me.connDeleteRequest(t, c, r)
			}
		case pp.Unchoke:
			c.PeerChoked = false
			me.peerUnchoked(t, c)
		case pp.Interested:
			c.PeerInterested = true
			// TODO: This should be done from a dedicated unchoking routine.
			if me.noUpload {
				break
			}
			c.Unchoke()
		case pp.NotInterested:
			c.PeerInterested = false
			c.Choke()
		case pp.Have:
			me.peerGotPiece(t, c, int(msg.Index))
		case pp.Request:
			if me.noUpload {
				break
			}
			if c.PeerRequests == nil {
				c.PeerRequests = make(map[request]struct{}, maxRequests)
			}
			request := newRequest(msg.Index, msg.Begin, msg.Length)
			// TODO: Requests should be satisfied from a dedicated upload routine.
			// c.PeerRequests[request] = struct{}{}
			p := make([]byte, msg.Length)
			n, err := t.Data.ReadAt(p, int64(t.PieceLength(0))*int64(msg.Index)+int64(msg.Begin))
			if err != nil {
				return fmt.Errorf("reading t data to serve request %q: %s", request, err)
			}
			if n != int(msg.Length) {
				return fmt.Errorf("bad request: %v", msg)
			}
			c.Post(pp.Message{
				Type:  pp.Piece,
				Index: msg.Index,
				Begin: msg.Begin,
				Piece: p,
			})
			uploadChunksPosted.Add(1)
		case pp.Cancel:
			req := newRequest(msg.Index, msg.Begin, msg.Length)
			if !c.PeerCancel(req) {
				unexpectedCancels.Add(1)
			}
		case pp.Bitfield:
			if c.PeerPieces != nil {
				err = errors.New("received unexpected bitfield")
				break
			}
			if t.haveInfo() {
				if len(msg.Bitfield) < t.NumPieces() {
					err = errors.New("received invalid bitfield")
					break
				}
				msg.Bitfield = msg.Bitfield[:t.NumPieces()]
			}
			c.PeerPieces = msg.Bitfield
			for index, has := range c.PeerPieces {
				if has {
					me.peerGotPiece(t, c, index)
				}
			}
		case pp.Piece:
			err = me.downloadedChunk(t, c, &msg)
		case pp.Extended:
			switch msg.ExtendedID {
			case pp.HandshakeExtendedID:
				// TODO: Create a bencode struct for this.
				var d map[string]interface{}
				err = bencode.Unmarshal(msg.ExtendedPayload, &d)
				if err != nil {
					err = fmt.Errorf("error decoding extended message payload: %s", err)
					break
				}
				// log.Printf("got handshake from %q: %#v", c.Socket.RemoteAddr().String(), d)
				if reqq, ok := d["reqq"]; ok {
					if i, ok := reqq.(int64); ok {
						c.PeerMaxRequests = int(i)
					}
				}
				if v, ok := d["v"]; ok {
					c.PeerClientName = v.(string)
				}
				m, ok := d["m"]
				if !ok {
					err = errors.New("handshake missing m item")
					break
				}
				mTyped, ok := m.(map[string]interface{})
				if !ok {
					err = errors.New("handshake m value is not dict")
					break
				}
				if c.PeerExtensionIDs == nil {
					c.PeerExtensionIDs = make(map[string]int64, len(mTyped))
				}
				for name, v := range mTyped {
					id, ok := v.(int64)
					if !ok {
						log.Printf("bad handshake m item extension ID type: %T", v)
						continue
					}
					if id == 0 {
						delete(c.PeerExtensionIDs, name)
					} else {
						c.PeerExtensionIDs[name] = id
					}
				}
				metadata_sizeUntyped, ok := d["metadata_size"]
				if ok {
					metadata_size, ok := metadata_sizeUntyped.(int64)
					if !ok {
						log.Printf("bad metadata_size type: %T", metadata_sizeUntyped)
					} else {
						t.SetMetadataSize(metadata_size)
					}
				}
				if _, ok := c.PeerExtensionIDs["ut_metadata"]; ok {
					me.requestPendingMetadata(t, c)
				}
			case 1:
				err = me.gotMetadataExtensionMsg(msg.ExtendedPayload, t, c)
				if err != nil {
					err = fmt.Errorf("error handling metadata extension message: %s", err)
				}
			case 2:
				var pexMsg peerExchangeMessage
				err := bencode.Unmarshal(msg.ExtendedPayload, &pexMsg)
				if err != nil {
					err = fmt.Errorf("error unmarshalling PEX message: %s", err)
					break
				}
				go func() {
					err := me.AddPeers(t.InfoHash, func() (ret []Peer) {
						for _, cp := range pexMsg.Added {
							p := Peer{
								IP:     make([]byte, 4),
								Port:   int(cp.Port),
								Source: peerSourcePEX,
							}
							if n := copy(p.IP, cp.IP[:]); n != 4 {
								panic(n)
							}
							ret = append(ret, p)
						}
						return
					}())
					if err != nil {
						log.Printf("error adding PEX peers: %s", err)
						return
					}
					peersFoundByPEX.Add(int64(len(pexMsg.Added)))
				}()
			default:
				err = fmt.Errorf("unexpected extended message ID: %v", msg.ExtendedID)
			}
			if err != nil {
				// That client uses its own extension IDs for outgoing message
				// types, which is incorrect.
				if bytes.HasPrefix(c.PeerID[:], []byte("-SD0100-")) ||
					strings.HasPrefix(string(c.PeerID[:]), "-XL0012-") {
					return nil
				}
				// log.Printf("peer extension map: %#v", c.PeerExtensionIDs)
			}
		case pp.Port:
			if me.dHT == nil {
				break
			}
			pingAddr, err := net.ResolveUDPAddr("", c.Socket.RemoteAddr().String())
			if err != nil {
				panic(err)
			}
			if msg.Port != 0 {
				pingAddr.Port = int(msg.Port)
			}
			_, err = me.dHT.Ping(pingAddr)
		default:
			err = fmt.Errorf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			return err
		}
	}
}

func (me *Client) dropConnection(torrent *torrent, conn *connection) {
	for r := range conn.Requests {
		me.connDeleteRequest(torrent, conn, r)
	}
	conn.Close()
	for i0, c := range torrent.Conns {
		if c != conn {
			continue
		}
		i1 := len(torrent.Conns) - 1
		if i0 != i1 {
			torrent.Conns[i0] = torrent.Conns[i1]
		}
		torrent.Conns = torrent.Conns[:i1]
		me.openNewConns(torrent)
		return
	}
	panic("connection not found")
}

func (me *Client) addConnection(t *torrent, c *connection) bool {
	if me.stopped() {
		return false
	}
	select {
	case <-t.ceasingNetworking:
		return false
	default:
	}
	for _, c0 := range t.Conns {
		if c.PeerID == c0.PeerID {
			// Already connected to a client with that ID.
			return false
		}
	}
	t.Conns = append(t.Conns, c)
	// TODO: This should probably be done by a routine that kills off bad
	// connections, and extra connections killed here instead.
	if len(t.Conns) > socketsPerTorrent {
		wcs := t.worstConnsHeap()
		heap.Pop(wcs).(*connection).Close()
	}
	return true
}

func (me *Client) openNewConns(t *torrent) {
	select {
	case <-t.ceasingNetworking:
		return
	default:
	}
	if t.haveInfo() && !me.downloadStrategy.PendingData(t) {
		return
	}
	for len(t.Peers) != 0 {
		if len(t.Conns) >= socketsPerTorrent {
			break
		}
		if len(t.HalfOpen)+me.handshaking >= me.halfOpenLimit {
			break
		}
		var (
			k peersKey
			p Peer
		)
		for k, p = range t.Peers {
			break
		}
		delete(t.Peers, k)
		me.initiateConn(p, t)
	}
	t.wantPeers.Broadcast()
}

// Adds peers to the swarm for the torrent corresponding to infoHash.
func (me *Client) AddPeers(infoHash InfoHash, peers []Peer) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	t := me.torrent(infoHash)
	if t == nil {
		return errors.New("no such torrent")
	}
	t.AddPeers(peers)
	me.openNewConns(t)
	return nil
}

func (cl *Client) setMetaData(t *torrent, md metainfo.Info, bytes []byte) (err error) {
	err = t.setMetadata(md, cl.dataDir, bytes)
	if err != nil {
		return
	}
	// If the client intends to upload, it needs to know what state pieces are
	// in.
	if !cl.noUpload {
		// Queue all pieces for hashing. This is done sequentially to avoid
		// spamming goroutines.
		for _, p := range t.Pieces {
			p.QueuedForHash = true
		}
		go func() {
			for i := range t.Pieces {
				cl.verifyPiece(t, pp.Integer(i))
			}
		}()
	}

	cl.downloadStrategy.TorrentStarted(t)
	select {
	case t.gotMetainfo <- &metainfo.MetaInfo{
		Info: metainfo.InfoEx{
			Info: md,
		},
		CreationDate: time.Now().Unix(),
		Comment:      "metadata set in client",
		CreatedBy:    "go.torrent",
		// TODO(anacrolix): Expose trackers given when torrent added.
	}:
	default:
		panic("shouldn't block")
	}
	close(t.gotMetainfo)
	t.gotMetainfo = nil
	return
}

// Prepare a Torrent without any attachment to a Client. That means we can
// initialize fields all fields that don't require the Client without locking
// it.
func newTorrent(ih InfoHash, announceList [][]string, halfOpenLimit int) (t *torrent, err error) {
	t = &torrent{
		InfoHash: ih,
		Peers:    make(map[peersKey]Peer, 2000),

		closing:           make(chan struct{}),
		ceasingNetworking: make(chan struct{}),

		gotMetainfo: make(chan *metainfo.MetaInfo, 1),

		HalfOpen: make(map[string]struct{}, halfOpenLimit),
	}
	t.wantPeers.L = &t.stateMu
	t.GotMetainfo = t.gotMetainfo
	t.addTrackers(announceList)
	return
}

// The trackers within each tier must be shuffled before use.
// http://stackoverflow.com/a/12267471/149482
// http://www.bittorrent.org/beps/bep_0012.html#order-of-processing
func shuffleTier(tier []tracker.Client) {
	for i := range tier {
		j := mathRand.Intn(i + 1)
		tier[i], tier[j] = tier[j], tier[i]
	}
}

func copyTrackers(base [][]tracker.Client) (copy [][]tracker.Client) {
	for _, tier := range base {
		copy = append(copy, tier)
	}
	return
}

func mergeTier(tier []tracker.Client, newURLs []string) []tracker.Client {
nextURL:
	for _, url := range newURLs {
		for _, tr := range tier {
			if tr.URL() == url {
				continue nextURL
			}
		}
		tr, err := tracker.New(url)
		if err != nil {
			log.Printf("error creating tracker client for %q: %s", url, err)
			continue
		}
		tier = append(tier, tr)
	}
	return tier
}

func (t *torrent) addTrackers(announceList [][]string) {
	newTrackers := copyTrackers(t.Trackers)
	for tierIndex, tier := range announceList {
		if tierIndex < len(newTrackers) {
			newTrackers[tierIndex] = mergeTier(newTrackers[tierIndex], tier)
		} else {
			newTrackers = append(newTrackers, mergeTier(nil, tier))
		}
		shuffleTier(newTrackers[tierIndex])
	}
	t.Trackers = newTrackers
}

type Torrent struct {
	cl *Client
	*torrent
}

func (me Torrent) ReadAt(p []byte, off int64) (n int, err error) {
	err = me.cl.PrioritizeDataRegion(me.InfoHash, off, int64(len(p)))
	if err != nil {
		err = fmt.Errorf("error prioritizing: %s", err)
		return
	}
	<-me.cl.DataWaiter(me.InfoHash, off)
	return me.cl.TorrentReadAt(me.InfoHash, off, p)
}

func (cl *Client) AddMagnet(uri string) (t Torrent, err error) {
	t.cl = cl
	m, err := ParseMagnetURI(uri)
	if err != nil {
		return
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	t.torrent = cl.torrent(m.InfoHash)
	if t.torrent != nil {
		t.addTrackers([][]string{m.Trackers})
		return
	}
	t.torrent, err = newTorrent(m.InfoHash, [][]string{m.Trackers}, cl.halfOpenLimit)
	if err != nil {
		return
	}
	t.DisplayName = m.DisplayName
	err = cl.addTorrent(t.torrent)
	if err != nil {
		t.Close()
	}
	go cl.connectionPruner(t.torrent)
	return
}

func (cl *Client) connectionPruner(t *torrent) {
	for {
		time.Sleep(15 * time.Second)
		cl.mu.Lock()
		license := len(t.Conns) - (socketsPerTorrent+1)/2
		for _, c := range t.Conns {
			if license <= 0 {
				break
			}
			if time.Now().Sub(c.lastUsefulChunkReceived) < time.Minute {
				continue
			}
			if time.Now().Sub(c.completedHandshake) < time.Minute {
				continue
			}
			c.Close()
			license--
		}
		cl.mu.Unlock()
	}
}

func (me *Client) DropTorrent(infoHash InfoHash) (err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	t, ok := me.torrents[infoHash]
	if !ok {
		err = fmt.Errorf("no such torrent")
		return
	}
	err = t.Close()
	if err != nil {
		panic(err)
	}
	delete(me.torrents, infoHash)
	me.downloadStrategy.TorrentStopped(t)
	for _, dw := range me.dataWaits[t] {
		close(dw.ready)
	}
	delete(me.dataWaits, t)
	return
}

func (me *Client) addTorrent(t *torrent) (err error) {
	if _, ok := me.torrents[t.InfoHash]; ok {
		err = fmt.Errorf("torrent infohash collision")
		return
	}
	me.torrents[t.InfoHash] = t
	if !me.disableTrackers {
		go me.announceTorrent(t)
	}
	if me.dHT != nil {
		go me.announceTorrentDHT(t, true)
	}
	return
}

// Adds the torrent to the client.
func (me *Client) AddTorrent(metaInfo *metainfo.MetaInfo) (err error) {
	var ih InfoHash
	CopyExact(&ih, metaInfo.Info.Hash)
	t, err := newTorrent(ih, metaInfo.AnnounceList, me.halfOpenLimit)
	if err != nil {
		return
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	err = me.addTorrent(t)
	if err != nil {
		return
	}
	err = me.setMetaData(t, metaInfo.Info.Info, metaInfo.Info.Bytes)
	if err != nil {
		return
	}
	return
}

func (me *Client) AddTorrentFromFile(name string) (err error) {
	mi, err := metainfo.LoadFromFile(name)
	if err != nil {
		err = fmt.Errorf("error loading metainfo from file: %s", err)
		return
	}
	return me.AddTorrent(mi)
}

// Returns true when peers are required, or false if the torrent is closing.
func (cl *Client) waitWantPeers(t *torrent) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	for {
		select {
		case <-t.ceasingNetworking:
			return false
		default:
		}
		if len(t.Peers) < socketsPerTorrent*5 {
			return true
		}
		cl.mu.Unlock()
		t.wantPeers.Wait()
		t.stateMu.Unlock()
		cl.mu.Lock()
		t.stateMu.Lock()
	}
}

func (cl *Client) announceTorrentDHT(t *torrent, impliedPort bool) {
	for cl.waitWantPeers(t) {
		log.Printf("announcing torrent %q to DHT", t)
		ps, err := cl.dHT.GetPeers(string(t.InfoHash[:]))
		if err != nil {
			log.Printf("error getting peers from dht: %s", err)
			return
		}
	getPeers:
		for {
			select {
			case v, ok := <-ps.Values:
				if !ok {
					break getPeers
				}
				peersFoundByDHT.Add(int64(len(v.Peers)))
				err = cl.AddPeers(t.InfoHash, func() (ret []Peer) {
					for _, cp := range v.Peers {
						ret = append(ret, Peer{
							IP:     cp.IP[:],
							Port:   int(cp.Port),
							Source: peerSourceDHT,
						})
					}
					return
				}())
				if err != nil {
					log.Printf("error adding peers from dht for torrent %q: %s", t, err)
					break getPeers
				}
			case <-t.ceasingNetworking:
				ps.Close()
				return
			}
		}
		ps.Close()
		log.Printf("finished DHT peer scrape for %s", t)

		// After a GetPeers, we can announce on the best nodes that gave us an
		// announce token.

		port := cl.incomingPeerPort()
		// If port is zero, then we're not listening, and there's nothing to
		// announce.
		if port != 0 {
			// We can't allow the port to be implied as long as the UTP and
			// DHT ports are different.
			err := cl.dHT.AnnouncePeer(port, impliedPort, t.InfoHash.AsString())
			if err != nil {
				log.Printf("error announcing torrent to DHT: %s", err)
			} else {
				log.Printf("announced %q to DHT", t)
			}
		}
	}
}

func (cl *Client) announceTorrent(t *torrent) {
	req := tracker.AnnounceRequest{
		Event:    tracker.Started,
		NumWant:  -1,
		Port:     int16(cl.incomingPeerPort()),
		PeerId:   cl.peerID,
		InfoHash: t.InfoHash,
	}
newAnnounce:
	for cl.waitWantPeers(t) {
		cl.mu.Lock()
		req.Left = t.BytesLeft()
		trackers := t.Trackers
		cl.mu.Unlock()
		for _, tier := range trackers {
			for trIndex, tr := range tier {
				if err := tr.Connect(); err != nil {
					log.Printf("error connecting to tracker at %q: %s", tr, err)
					continue
				}
				resp, err := tr.Announce(&req)
				if err != nil {
					log.Print(err)
					continue
				}
				var peers []Peer
				for _, peer := range resp.Peers {
					peers = append(peers, Peer{
						IP:   peer.IP,
						Port: peer.Port,
					})
				}
				err = cl.AddPeers(t.InfoHash, peers)
				if err != nil {
					log.Printf("error adding peers to torrent %s: %s", t, err)
				} else {
					log.Printf("%s: %d new peers from %s", t, len(peers), tr)
				}
				tier[0], tier[trIndex] = tier[trIndex], tier[0]
				time.Sleep(time.Second * time.Duration(resp.Interval))
				req.Event = tracker.None
				continue newAnnounce
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func (cl *Client) allTorrentsCompleted() bool {
	for _, t := range cl.torrents {
		if !t.haveInfo() {
			return false
		}
		for e := t.IncompletePiecesByBytesLeft.Front(); e != nil; e = e.Next() {
			i := e.Value.(int)
			if t.Pieces[i].Complete() {
				continue
			}
			// If the piece isn't complete, make sure it's not because it's
			// never been hashed.
			cl.queueFirstHash(t, i)
			return false
		}
	}
	return true
}

// Returns true when all torrents are completely downloaded and false if the
// client is stopped before that.
func (me *Client) WaitAll() bool {
	me.mu.Lock()
	defer me.mu.Unlock()
	for !me.allTorrentsCompleted() {
		if me.stopped() {
			return false
		}
		me.event.Wait()
	}
	return true
}

func (cl *Client) assertRequestHeat() {
	dds, ok := cl.downloadStrategy.(*DefaultDownloadStrategy)
	if !ok {
		return
	}
	for _, t := range cl.torrents {
		m := make(map[request]int, 3000)
		for _, cn := range t.Conns {
			for r := range cn.Requests {
				m[r]++
			}
		}
		for r, h := range dds.heat[t] {
			if m[r] != h {
				panic(fmt.Sprintln(m[r], h))
			}
		}
	}
}

func (me *Client) replenishConnRequests(t *torrent, c *connection) {
	if !t.haveInfo() {
		return
	}
	for _, p := range me.downloadStrategy.FillRequests(t, c) {
		// Make sure the state of pieces that would have been requested is
		// known.
		me.queueFirstHash(t, p)
	}
	//me.assertRequestHeat()
	if len(c.Requests) == 0 && !c.PeerChoked {
		c.SetInterested(false)
	}
}

// Handle a received chunk from a peer.
func (me *Client) downloadedChunk(t *torrent, c *connection, msg *pp.Message) error {
	chunksDownloadedCount.Add(1)

	req := newRequest(msg.Index, msg.Begin, pp.Integer(len(msg.Piece)))

	// Request has been satisfied.
	me.connDeleteRequest(t, c, req)

	defer me.replenishConnRequests(t, c)

	// Do we actually want this chunk?
	if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
		unusedDownloadedChunksCount.Add(1)
		c.UnwantedChunksReceived++
		return nil
	}

	c.UsefulChunksReceived++
	c.lastUsefulChunkReceived = time.Now()

	// Write the chunk out.
	err := t.WriteChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	if err != nil {
		return fmt.Errorf("error writing chunk: %s", err)
	}

	// Record that we have the chunk.
	delete(t.Pieces[req.Index].PendingChunkSpecs, req.chunkSpec)
	me.dataReady(t, req)
	if len(t.Pieces[req.Index].PendingChunkSpecs) == 0 {
		me.queuePieceCheck(t, req.Index)
	}
	t.PieceBytesLeftChanged(int(req.Index))

	// Unprioritize the chunk.
	me.downloadStrategy.TorrentGotChunk(t, req)

	// Cancel pending requests for this chunk.
	for _, c := range t.Conns {
		if me.connCancel(t, c, req) {
			me.replenishConnRequests(t, c)
		}
	}

	me.downloadStrategy.AssertNotRequested(t, req)

	return nil
}

func (cl *Client) dataReady(t *torrent, r request) {
	dws := cl.dataWaits[t]
	begin := t.requestOffset(r)
	end := begin + int64(r.Length)
	for i := 0; i < len(dws); {
		dw := dws[i]
		if begin <= dw.offset && dw.offset < end {
			close(dw.ready)
			dws[i] = dws[len(dws)-1]
			dws = dws[:len(dws)-1]
		} else {
			i++
		}
	}
	cl.dataWaits[t] = dws
}

// Returns a channel that is closed when new data has become available in the
// client.
func (me *Client) DataWaiter(ih InfoHash, off int64) (ret <-chan struct{}) {
	me.mu.Lock()
	defer me.mu.Unlock()
	ch := make(chan struct{})
	ret = ch
	t := me.torrents[ih]
	if t == nil {
		close(ch)
		return
	}
	if r, ok := t.offsetRequest(off); !ok || t.haveChunk(r) {
		close(ch)
		return
	}
	me.dataWaits[t] = append(me.dataWaits[t], dataWait{
		offset: off,
		ready:  ch,
	})
	return
}

func (me *Client) pieceHashed(t *torrent, piece pp.Integer, correct bool) {
	p := t.Pieces[piece]
	if p.EverHashed && !correct {
		log.Printf("%s: piece %d failed hash", t, piece)
		failedPieceHashes.Add(1)
	}
	p.EverHashed = true
	if correct {
		p.PendingChunkSpecs = nil
		me.downloadStrategy.TorrentGotPiece(t, int(piece))
		me.dataReady(t, request{
			pp.Integer(piece),
			chunkSpec{0, pp.Integer(t.PieceLength(piece))},
		})
	} else {
		if len(p.PendingChunkSpecs) == 0 {
			t.pendAllChunkSpecs(piece)
		}
	}
	t.PieceBytesLeftChanged(int(piece))
	for _, conn := range t.Conns {
		if correct {
			conn.Post(pp.Message{
				Type:  pp.Have,
				Index: pp.Integer(piece),
			})
			// TODO: Cancel requests for this piece.
			for r := range conn.Requests {
				if r.Index == piece {
					panic("wat")
				}
			}
		}
		// Do this even if the piece is correct because new first-hashings may
		// need to be scheduled.
		if conn.PeerHasPiece(piece) {
			me.replenishConnRequests(t, conn)
		}
	}
	if t.haveAllPieces() && me.noUpload {
		t.CeaseNetworking()
	}
	me.event.Broadcast()
}

func (cl *Client) verifyPiece(t *torrent, index pp.Integer) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	p := t.Pieces[index]
	for p.Hashing {
		cl.event.Wait()
	}
	if t.isClosed() {
		return
	}
	p.Hashing = true
	p.QueuedForHash = false
	cl.mu.Unlock()
	sum := t.HashPiece(index)
	cl.mu.Lock()
	select {
	case <-t.closing:
		return
	default:
	}
	p.Hashing = false
	cl.pieceHashed(t, index, sum == p.Hash)
}

func (me *Client) Torrents() (ret []*torrent) {
	me.mu.Lock()
	for _, t := range me.torrents {
		ret = append(ret, t)
	}
	me.mu.Unlock()
	return
}
