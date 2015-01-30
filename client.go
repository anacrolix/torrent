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
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"bitbucket.org/anacrolix/go.torrent/dht"
	"bitbucket.org/anacrolix/go.torrent/internal/pieceordering"
	"bitbucket.org/anacrolix/go.torrent/iplist"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
	. "bitbucket.org/anacrolix/go.torrent/util"
	"bitbucket.org/anacrolix/sync"
	"bitbucket.org/anacrolix/utp"
	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/libtorgo/metainfo"
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
	inboundConnsBlocked         = expvar.NewInt("inboundConnsBlocked")
)

const (
	// Justification for set bits follows.
	//
	// Extension protocol: http://www.bittorrent.org/beps/bep_0010.html
	// DHT: http://www.bittorrent.org/beps/bep_0005.html
	extensionBytes = "\x00\x00\x00\x00\x00\x10\x00\x01"

	socketsPerTorrent     = 40
	torrentPeersHighWater = 1000
	torrentPeersLowWater  = socketsPerTorrent * 5
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
	firstIndex := int(off / int64(t.UsualPieceSize()))
	for i := 0; i < 5; i++ {
		index := firstIndex + i
		if index >= t.NumPieces() {
			continue
		}
		me.queueFirstHash(t, index)
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
	utpSock          *utp.Socket
	disableTrackers  bool
	downloadStrategy DownloadStrategy
	dHT              *dht.Server
	disableUTP       bool
	disableTCP       bool
	ipBlockList      *iplist.IPList
	bannedTorrents   map[InfoHash]struct{}

	mu    sync.RWMutex
	event sync.Cond
	quit  chan struct{}

	handshaking int

	torrents map[InfoHash]*torrent

	dataWaits map[*torrent][]dataWait
}

func (me *Client) SetIPBlockList(list *iplist.IPList) {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.ipBlockList = list
	if me.dHT != nil {
		me.dHT.SetIPBlockList(list)
	}
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
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	w := bufio.NewWriter(_w)
	defer w.Flush()
	fmt.Fprintf(w, "Listening on %s\n", cl.ListenAddr())
	fmt.Fprintf(w, "Peer ID: %q\n", cl.peerID)
	fmt.Fprintf(w, "Handshaking: %d\n", cl.handshaking)
	if cl.dHT != nil {
		dhtStats := cl.dHT.Stats()
		fmt.Fprintf(w, "DHT nodes: %d (%d good)\n", dhtStats.NumNodes, dhtStats.NumGoodNodes)
		fmt.Fprintf(w, "DHT Server ID: %x\n", cl.dHT.IDString())
		fmt.Fprintf(w, "DHT port: %d\n", addrPort(cl.dHT.LocalAddr()))
		fmt.Fprintf(w, "DHT announces: %d\n", cl.dHT.NumConfirmedAnnounces)
		fmt.Fprintf(w, "Outstanding transactions: %d\n", dhtStats.NumOutstandingTransactions)
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
func (cl *Client) torrentReadAt(t *torrent, off int64, p []byte) (n int, err error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	index := int(off / int64(t.UsualPieceSize()))
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
	pieceLeft := int(t.PieceLength(pp.Integer(index)) - pieceOff)
	if pieceLeft <= 0 {
		err = io.EOF
		return
	}
	cl.readRaisePiecePriorities(t, off, int64(len(p)))
	if len(p) > pieceLeft {
		p = p[:pieceLeft]
	}
	if len(p) == 0 {
		panic(len(p))
	}
	for !piece.Complete() {
		piece.Event.Wait()
	}
	return t.Data.ReadAt(p, off)
}

func (cl *Client) readRaisePiecePriorities(t *torrent, off, _len int64) {
	index := int(off / int64(t.UsualPieceSize()))
	cl.raisePiecePriority(t, index, piecePriorityNow)
	index++
	if index >= t.NumPieces() {
		return
	}
	cl.raisePiecePriority(t, index, piecePriorityNext)
	for i := 0; i < t.numConnsUnchoked()-2; i++ {
		index++
		if index >= t.NumPieces() {
			break
		}
		cl.raisePiecePriority(t, index, piecePriorityReadahead)
	}
}

func (cl *Client) configDir() string {
	return filepath.Join(os.Getenv("HOME"), ".config/torrent")
}

func (cl *Client) ConfigDir() string {
	return cl.configDir()
}

func (t *torrent) connPendPiece(c *connection, piece int) {
	c.pendPiece(piece, t.Pieces[piece].Priority)
}

func (cl *Client) raisePiecePriority(t *torrent, piece int, priority piecePriority) {
	if t.Pieces[piece].Priority < priority {
		cl.prioritizePiece(t, piece, priority)
	}
}

func (cl *Client) prioritizePiece(t *torrent, piece int, priority piecePriority) {
	if t.havePiece(piece) {
		return
	}
	cl.queueFirstHash(t, piece)
	t.Pieces[piece].Priority = priority
	if t.wantPiece(piece) {
		for _, c := range t.Conns {
			if c.PeerHasPiece(pp.Integer(piece)) {
				t.connPendPiece(c, piece)
				cl.replenishConnRequests(t, c)
			}
		}
	}
}

func (cl *Client) setEnvBlocklist() (err error) {
	filename := os.Getenv("TORRENT_BLOCKLIST_FILE")
	defaultBlocklist := filename == ""
	if defaultBlocklist {
		filename = filepath.Join(cl.configDir(), "blocklist")
	}
	f, err := os.Open(filename)
	if err != nil {
		if defaultBlocklist {
			err = nil
		}
		return
	}
	defer f.Close()
	var ranges []iplist.Range
	uniqStrs := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		r, ok, lineErr := iplist.ParseBlocklistP2PLine(scanner.Bytes())
		if lineErr != nil {
			err = fmt.Errorf("error reading torrent blocklist line: %s", lineErr)
			return
		}
		if !ok {
			continue
		}
		if s, ok := uniqStrs[r.Description]; ok {
			r.Description = s
		} else {
			uniqStrs[r.Description] = r.Description
		}
		ranges = append(ranges, r)
	}
	err = scanner.Err()
	if err != nil {
		err = fmt.Errorf("error reading torrent blocklist: %s", err)
		return
	}
	cl.ipBlockList = iplist.New(ranges)
	return
}

func (cl *Client) initBannedTorrents() error {
	f, err := os.Open(filepath.Join(cl.configDir(), "banned_infohashes"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("error opening banned infohashes file: %s", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	cl.bannedTorrents = make(map[InfoHash]struct{})
	for scanner.Scan() {
		var ihs string
		n, err := fmt.Sscanf(scanner.Text(), "%x", &ihs)
		if err != nil {
			return fmt.Errorf("error reading infohash: %s", err)
		}
		if n != 1 {
			continue
		}
		if len(ihs) != 20 {
			return errors.New("bad infohash")
		}
		var ih InfoHash
		CopyExact(&ih, ihs)
		cl.bannedTorrents[ih] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error scanning file: %s", err)
	}
	return nil
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
		disableTCP:       cfg.DisableTCP,

		quit:     make(chan struct{}),
		torrents: make(map[InfoHash]*torrent),

		dataWaits: make(map[*torrent][]dataWait),
	}
	// TODO: Write my own UTP library ಠ_ಠ
	// cl.disableUTP = true
	cl.event.L = &cl.mu

	if !cfg.NoDefaultBlocklist {
		err = cl.setEnvBlocklist()
		if err != nil {
			return
		}
	}

	if err = cl.initBannedTorrents(); err != nil {
		err = fmt.Errorf("error initing banned torrents: %s", err)
		return
	}

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
	if !cl.disableTCP {
		var l net.Listener
		l, err = net.Listen("tcp", listenAddr())
		if err != nil {
			return
		}
		cl.listeners = append(cl.listeners, l)
		go cl.acceptConnections(l, false)
	}
	if !cl.disableUTP {
		cl.utpSock, err = utp.NewSocket(listenAddr())
		if err != nil {
			return
		}
		cl.listeners = append(cl.listeners, cl.utpSock)
		go cl.acceptConnections(cl.utpSock, true)
	}
	if !cfg.NoDHT {
		dhtCfg := cfg.DHTConfig
		if dhtCfg == nil {
			dhtCfg = &dht.ServerConfig{}
		}
		if dhtCfg.Addr == "" {
			dhtCfg.Addr = listenAddr()
		}
		if dhtCfg.Conn == nil && cl.utpSock != nil {
			dhtCfg.Conn = cl.utpSock
		}
		cl.dHT, err = dht.NewServer(dhtCfg)
		if cl.ipBlockList != nil {
			cl.dHT.SetIPBlockList(cl.ipBlockList)
		}
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

var ipv6BlockRange = iplist.Range{Description: "non-IPv4 address"}

func (cl *Client) ipBlockRange(ip net.IP) (r *iplist.Range) {
	if cl.ipBlockList == nil {
		return
	}
	ip = ip.To4()
	if ip == nil {
		log.Printf("saw non-IPv4 address")
		r = &ipv6BlockRange
		return
	}
	r = cl.ipBlockList.Lookup(ip)
	return
}

func (cl *Client) acceptConnections(l net.Listener, utp bool) {
	for {
		// We accept all connections immediately, because we don't know what
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
		cl.mu.RLock()
		blockRange := cl.ipBlockRange(AddrIP(conn.RemoteAddr()))
		cl.mu.RUnlock()
		if blockRange != nil {
			inboundConnsBlocked.Add(1)
			log.Printf("inbound connection from %s blocked by %s", conn.RemoteAddr(), blockRange)
			conn.Close()
			continue
		}
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
		if conn != nil {
			conn.Close()
		}
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
	if r := me.ipBlockRange(peer.IP); r != nil {
		log.Printf("outbound connect to %s blocked by IP blocklist rule %s", peer.IP, r)
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
		left := 0
		if !me.disableUTP {
			left++
		}
		if !me.disableTCP {
			left++
		}
		resCh := make(chan dialResult, left)
		if !me.disableUTP {
			go doDial(func() (net.Conn, error) {
				return me.utpSock.DialTimeout(addr, dialTimeout)
			}, resCh, true)
		}
		if !me.disableTCP {
			go doDial(func() (net.Conn, error) {
				// time.Sleep(time.Second) // Give uTP a bit of a head start.
				return net.DialTimeout("tcp", addr, dialTimeout)
			}, resCh, false)
		}
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
		err = fmt.Errorf("error setting read deadline: %s", err)
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
		conn.Post(pp.Message{
			Type: pp.Port,
			Port: uint16(AddrPort(me.dHT.LocalAddr())),
		})
	}
	if torrent.haveInfo() {
		torrent.initRequestOrdering(conn)
		me.replenishConnRequests(torrent, conn)
	}
	err = me.connectionLoop(torrent, conn)
	if err != nil {
		err = fmt.Errorf("during Connection loop with peer %q: %s", conn.PeerID, err)
	}
	me.dropConnection(torrent, conn)
	return
}

func (t *torrent) initRequestOrdering(c *connection) {
	if c.pieceRequestOrder != nil || c.piecePriorities != nil {
		panic("double init of request ordering")
	}
	c.piecePriorities = mathRand.Perm(t.NumPieces())
	c.pieceRequestOrder = pieceordering.New()
	for i := 0; i < t.NumPieces(); i++ {
		if !c.PeerHasPiece(pp.Integer(i)) {
			continue
		}
		if !t.wantPiece(i) {
			continue
		}
		t.connPendPiece(c, i)
	}
}

func (me *Client) peerGotPiece(t *torrent, c *connection, piece int) {
	if t.haveInfo() {
		if c.PeerPieces == nil {
			c.PeerPieces = make([]bool, t.NumPieces())
		}
	} else {
		for piece >= len(c.PeerPieces) {
			c.PeerPieces = append(c.PeerPieces, false)
		}
	}
	c.PeerPieces[piece] = true
	if t.wantPiece(piece) {
		t.connPendPiece(c, piece)
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
		c.UsefulChunksReceived++
		c.lastUsefulChunkReceived = time.Now()
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
		R:         bufio.NewReader(c.Socket),
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
			// We can then reset our interest.
			me.replenishConnRequests(t, c)
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
	if !me.wantConns(t) {
		return false
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

func (t *torrent) needData() bool {
	if !t.haveInfo() {
		return true
	}
	for i := range t.Pieces {
		if t.wantPiece(i) {
			return true
		}
	}
	return false
}

// TODO: I'm sure there's something here to do with seeding.
func (t *torrent) badConn(c *connection) bool {
	if time.Now().Sub(c.completedHandshake) < 30*time.Second {
		return false
	}
	if !t.haveInfo() {
		return !c.supportsExtension("ut_metadata")
	}
	return !t.connHasWantedPieces(c)
}

func (t *torrent) numGoodConns() (num int) {
	for _, c := range t.Conns {
		if !t.badConn(c) {
			num++
		}
	}
	return
}

func (me *Client) wantConns(t *torrent) bool {
	if !t.needData() && me.noUpload {
		return false
	}
	if t.numGoodConns() >= socketsPerTorrent {
		return false
	}
	return true
}

func (me *Client) openNewConns(t *torrent) {
	select {
	case <-t.ceasingNetworking:
		return
	default:
	}
	for len(t.Peers) != 0 {
		if !me.wantConns(t) {
			return
		}
		if len(t.HalfOpen) >= me.halfOpenLimit {
			return
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

func (me *Client) addPeers(t *torrent, peers []Peer) {
	blocked := 0
	for i, p := range peers {
		if me.ipBlockRange(p.IP) == nil {
			continue
		}
		peers[i] = peers[len(peers)-1]
		peers = peers[:len(peers)-1]
		i--
		blocked++
	}
	if blocked != 0 {
		log.Printf("IP blocklist screened %d peers from being added", blocked)
	}
	t.AddPeers(peers)
	me.openNewConns(t)
}

// Adds peers to the swarm for the torrent corresponding to infoHash.
func (me *Client) AddPeers(infoHash InfoHash, peers []Peer) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	t := me.torrent(infoHash)
	if t == nil {
		return errors.New("no such torrent")
	}
	me.addPeers(t, peers)
	return nil
}

func (cl *Client) torrentFileCachePath(ih InfoHash) string {
	return filepath.Join(cl.configDir(), "torrents", ih.HexString()+".torrent")
}

func (cl *Client) saveTorrentFile(t *torrent) error {
	path := cl.torrentFileCachePath(t.InfoHash)
	os.MkdirAll(filepath.Dir(path), 0777)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return fmt.Errorf("error opening file: %s", err)
	}
	defer f.Close()
	e := bencode.NewEncoder(f)
	err = e.Encode(t.MetaInfo())
	if err != nil {
		return fmt.Errorf("error marshalling metainfo: %s", err)
	}
	mi, _ := cl.torrentCacheMetaInfo(t.InfoHash)
	if !bytes.Equal(mi.Info.Hash, t.InfoHash[:]) {
		log.Fatalf("%x != %x", mi.Info.Hash, t.InfoHash[:])
	}
	return nil
}

func (cl *Client) setMetaData(t *torrent, md metainfo.Info, bytes []byte) (err error) {
	err = t.setMetadata(md, cl.dataDir, bytes, &cl.mu)
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
	if err := cl.saveTorrentFile(t); err != nil {
		log.Printf("error saving torrent file for %s: %s", t, err)
	}
	close(t.gotMetainfo)
	return
}

// Prepare a Torrent without any attachment to a Client. That means we can
// initialize fields all fields that don't require the Client without locking
// it.
func newTorrent(ih InfoHash, announceList [][]string, halfOpenLimit int) (t *torrent, err error) {
	t = &torrent{
		InfoHash: ih,
		Peers:    make(map[peersKey]Peer),

		closing:           make(chan struct{}),
		ceasingNetworking: make(chan struct{}),

		gotMetainfo: make(chan struct{}),

		HalfOpen: make(map[string]struct{}),
	}
	t.wantPeers.L = &t.stateMu
	t.GotMetainfo = t.gotMetainfo
	t.addTrackers(announceList)
	return
}

func init() {
	// For shuffling the tracker tiers.
	mathRand.Seed(time.Now().Unix())
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
		copy = append(copy, append([]tracker.Client{}, tier...))
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

type File struct {
	t      Torrent
	path   string
	offset int64
	length int64
}

func (f *File) Length() int64 {
	return f.length
}

func (f *File) PrioritizeRegion(off, len int64) {
	if off < 0 || off >= f.length {
		return
	}
	if off+len > f.length {
		len = f.length - off
	}
	off += f.offset
	f.t.SetRegionPriority(off, len)
}

// Returns handles to the files in the torrent. This requires the metainfo is
// available first.
func (t Torrent) Files() (ret []File) {
	var offset int64
	for _, fi := range t.Info.UpvertedFiles() {
		ret = append(ret, File{
			t,
			strings.Join(fi.Path, "/"),
			offset,
			fi.Length,
		})
		offset += fi.Length
	}
	return
}

func (t Torrent) SetRegionPriority(off, len int64) {
	t.cl.mu.Lock()
	defer t.cl.mu.Unlock()
	pieceSize := int64(t.UsualPieceSize())
	for i := off / pieceSize; i*pieceSize < off+len; i++ {
		t.cl.prioritizePiece(t.torrent, int(i), piecePriorityNormal)
	}
}

func (t Torrent) MetainfoFilepath() string {
	return filepath.Join(t.cl.ConfigDir(), "torrents", t.InfoHash.HexString()+".torrent")
}

func (t Torrent) AddPeers(pp []Peer) error {
	return t.cl.AddPeers(t.torrent.InfoHash, pp)
}

func (t Torrent) DownloadAll() {
	t.cl.mu.Lock()
	for i := 0; i < t.NumPieces(); i++ {
		// TODO: Leave higher priorities as they were?
		t.cl.prioritizePiece(t.torrent, i, piecePriorityNormal)
	}
	// Nice to have the first and last pieces soon for various interactive
	// purposes.
	t.cl.prioritizePiece(t.torrent, 0, piecePriorityReadahead)
	t.cl.prioritizePiece(t.torrent, t.NumPieces()-1, piecePriorityReadahead)
	t.cl.mu.Unlock()
}

func (me Torrent) ReadAt(p []byte, off int64) (n int, err error) {
	return me.cl.torrentReadAt(me.torrent, off, p)
}

func (cl *Client) torrentCacheMetaInfo(ih InfoHash) (mi *metainfo.MetaInfo, err error) {
	f, err := os.Open(cl.torrentFileCachePath(ih))
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return
	}
	defer f.Close()
	dec := bencode.NewDecoder(f)
	err = dec.Decode(&mi)
	if err != nil {
		return
	}
	if !bytes.Equal(mi.Info.Hash, ih[:]) {
		err = fmt.Errorf("cached torrent has wrong infohash: %x != %x", mi.Info.Hash, ih[:])
		return
	}
	return
}

func (cl *Client) AddMagnet(uri string) (T Torrent, err error) {
	m, err := ParseMagnetURI(uri)
	if err != nil {
		return
	}
	mi, err := cl.torrentCacheMetaInfo(m.InfoHash)
	if err != nil {
		log.Printf("error getting cached metainfo for %x: %s", m.InfoHash[:], err)
	} else if mi != nil {
		cl.AddTorrent(mi)
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	T, err = cl.addOrMergeTorrent(m.InfoHash, [][]string{m.Trackers})
	if err != nil {
		return
	}
	if m.DisplayName != "" {
		T.DisplayName = m.DisplayName
	}
	return
}

// Actively prunes unused connections. This is required to make space to dial
// for replacements.
func (cl *Client) connectionPruner(t *torrent) {
	for {
		time.Sleep(15 * time.Second)
		cl.mu.Lock()
		select {
		case <-t.ceasingNetworking:
		default:
		}
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

func (me *Client) addOrMergeTorrent(ih InfoHash, announceList [][]string) (T Torrent, err error) {
	if _, ok := me.bannedTorrents[ih]; ok {
		err = errors.New("banned torrent")
		return
	}
	T.cl = me
	var ok bool
	T.torrent, ok = me.torrents[ih]
	if ok {
		T.torrent.addTrackers(announceList)
	} else {
		T.torrent, err = newTorrent(ih, announceList, me.halfOpenLimit)
		if err != nil {
			return
		}
		me.torrents[ih] = T.torrent
		if !me.disableTrackers {
			go me.announceTorrentTrackers(T.torrent)
		}
		if me.dHT != nil {
			go me.announceTorrentDHT(T.torrent, true)
		}
		go me.connectionPruner(T.torrent)
	}
	return
}

// Adds the torrent to the client.
func (me *Client) AddTorrent(metaInfo *metainfo.MetaInfo) (t Torrent, err error) {
	var ih InfoHash
	CopyExact(&ih, metaInfo.Info.Hash)
	me.mu.Lock()
	defer me.mu.Unlock()
	t, err = me.addOrMergeTorrent(ih, metaInfo.AnnounceList)
	if err != nil {
		return
	}
	if !t.torrent.haveInfo() {
		err = me.setMetaData(t.torrent, metaInfo.Info.Info, metaInfo.Info.Bytes)
		if err != nil {
			return
		}
	}
	return
}

func (me *Client) AddTorrentFromFile(name string) (t Torrent, err error) {
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
		if len(t.Peers) < torrentPeersLowWater {
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
		log.Printf("getting peers for %q from DHT", t)
		ps, err := cl.dHT.Announce(string(t.InfoHash[:]), cl.incomingPeerPort(), impliedPort)
		if err != nil {
			log.Printf("error getting peers from dht: %s", err)
			return
		}
		allAddrs := make(map[string]struct{})
	getPeers:
		for {
			select {
			case v, ok := <-ps.Values:
				if !ok {
					break getPeers
				}
				peersFoundByDHT.Add(int64(len(v.Peers)))
				for _, p := range v.Peers {
					allAddrs[(&net.UDPAddr{
						IP:   p.IP[:],
						Port: int(p.Port),
					}).String()] = struct{}{}
				}
				// log.Printf("%s: %d new peers from DHT", t, len(v.Peers))
				cl.mu.Lock()
				cl.addPeers(t, func() (ret []Peer) {
					for _, cp := range v.Peers {
						ret = append(ret, Peer{
							IP:     cp.IP[:],
							Port:   int(cp.Port),
							Source: peerSourceDHT,
						})
					}
					return
				}())
				numPeers := len(t.Peers)
				cl.mu.Unlock()
				if numPeers >= torrentPeersHighWater {
					break getPeers
				}
			case <-t.ceasingNetworking:
				ps.Close()
				return
			}
		}
		ps.Close()
		log.Printf("finished DHT peer scrape for %s: %d peers", t, len(allAddrs))

	}
}

func (cl *Client) announceTorrentSingleTracker(tr tracker.Client, req *tracker.AnnounceRequest, t *torrent) error {
	if err := tr.Connect(); err != nil {
		return fmt.Errorf("error connecting: %s", err)
	}
	resp, err := tr.Announce(req)
	if err != nil {
		return fmt.Errorf("error announcing: %s", err)
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

	time.Sleep(time.Second * time.Duration(resp.Interval))
	return nil
}

func (cl *Client) announceTorrentTrackersFastStart(req *tracker.AnnounceRequest, trackers [][]tracker.Client, t *torrent) (atLeastOne bool) {
	oks := make(chan bool)
	outstanding := 0
	for _, tier := range trackers {
		for _, tr := range tier {
			outstanding++
			go func(tr tracker.Client) {
				err := cl.announceTorrentSingleTracker(tr, req, t)
				oks <- err == nil
			}(tr)
		}
	}
	for outstanding > 0 {
		ok := <-oks
		outstanding--
		if ok {
			atLeastOne = true
		}
	}
	return
}

// Announce torrent to its trackers.
func (cl *Client) announceTorrentTrackers(t *torrent) {
	req := tracker.AnnounceRequest{
		Event:    tracker.Started,
		NumWant:  -1,
		Port:     int16(cl.incomingPeerPort()),
		PeerId:   cl.peerID,
		InfoHash: t.InfoHash,
	}
	cl.mu.RLock()
	req.Left = t.BytesLeft()
	trackers := t.Trackers
	cl.mu.RUnlock()
	if cl.announceTorrentTrackersFastStart(&req, trackers, t) {
		req.Event = tracker.None
	}
newAnnounce:
	for cl.waitWantPeers(t) {
		cl.mu.RLock()
		req.Left = t.BytesLeft()
		trackers = t.Trackers
		cl.mu.RUnlock()
		numTrackersTried := 0
		for _, tier := range trackers {
			for trIndex, tr := range tier {
				numTrackersTried++
				err := cl.announceTorrentSingleTracker(tr, &req, t)
				if err != nil {
					continue
				}
				// Float the successful announce to the top of the tier. If
				// the trackers list has been changed, we'll be modifying an
				// old copy so it won't matter.
				cl.mu.Lock()
				tier[0], tier[trIndex] = tier[trIndex], tier[0]
				cl.mu.Unlock()

				req.Event = tracker.None
				continue newAnnounce
			}
		}
		if numTrackersTried != 0 {
			log.Printf("%s: all trackers failed", t)
		}
		// TODO: Wait until trackers are added if there are none.
		time.Sleep(10 * time.Second)
	}
}

func (cl *Client) allTorrentsCompleted() bool {
	for _, t := range cl.torrents {
		if !t.haveInfo() {
			return false
		}
		if t.NumPiecesCompleted() != t.NumPieces() {
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

func (me *Client) replenishConnRequests(t *torrent, c *connection) {
	if !t.haveInfo() {
		return
	}
	me.downloadStrategy.FillRequests(t, c)
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
		for _, c := range t.Conns {
			c.pieceRequestOrder.DeletePiece(int(req.Index))
		}
		me.queuePieceCheck(t, req.Index)
	}

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
		p.Priority = piecePriorityNone
		p.PendingChunkSpecs = nil
		p.Event.Broadcast()
		me.downloadStrategy.TorrentGotPiece(t, int(piece))
		me.dataReady(t, request{
			pp.Integer(piece),
			chunkSpec{0, pp.Integer(t.PieceLength(piece))},
		})
	} else {
		if len(p.PendingChunkSpecs) == 0 {
			t.pendAllChunkSpecs(piece)
		}
		if p.Priority != piecePriorityNone {
			me.openNewConns(t)
		}
	}
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
			conn.pieceRequestOrder.DeletePiece(int(piece))
		}
		if t.wantPiece(int(piece)) && conn.PeerHasPiece(piece) {
			t.connPendPiece(conn, int(piece))
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

func (me *Client) Torrents() (ret []Torrent) {
	me.mu.Lock()
	for _, t := range me.torrents {
		ret = append(ret, Torrent{me, t})
	}
	me.mu.Unlock()
	return
}
