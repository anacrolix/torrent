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
	"bitbucket.org/anacrolix/go.torrent/dht"
	"bitbucket.org/anacrolix/go.torrent/util"
	"bufio"
	"container/list"
	"crypto/rand"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"log"
	mathRand "math/rand"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/libtorgo/metainfo"
	"github.com/nsf/libtorgo/bencode"

	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
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
	newPriorities := make([]request, 0, (len_+chunkSize-1)/chunkSize)
	for len_ > 0 {
		req, ok := t.offsetRequest(off)
		if !ok {
			return errors.New("bad offset")
		}
		reqOff := t.requestOffset(req)
		// Gain the alignment adjustment.
		len_ += off - reqOff
		// Lose the length of this block.
		len_ -= int64(req.Length)
		off = reqOff + int64(req.Length)
		if !t.wantPiece(int(req.Index)) {
			continue
		}
		newPriorities = append(newPriorities, req)
	}
	if len(newPriorities) == 0 {
		return nil
	}
	t.Priorities.PushFront(newPriorities[0])
	for _, req := range newPriorities[1:] {
		t.Priorities.PushBack(req)
	}
	for _, cn := range t.Conns {
		me.replenishConnRequests(t, cn)
	}
	return nil
}

type dataSpec struct {
	InfoHash
	request
}

type Client struct {
	DataDir          string
	HalfOpenLimit    int
	PeerId           [20]byte
	Listener         net.Listener
	DisableTrackers  bool
	DownloadStrategy DownloadStrategy
	DHT              *dht.Server

	mu    sync.Mutex
	event sync.Cond
	quit  chan struct{}

	halfOpen   int
	torrents   map[InfoHash]*torrent
	dataWaiter chan struct{}
}

func (cl *Client) WriteStatus(w io.Writer) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	fmt.Fprintf(w, "Half open: %d\n", cl.halfOpen)
	fmt.Fprintf(w, "DHT nodes: %d\n", cl.DHT.NumNodes())
	fmt.Fprintln(w)
	for _, t := range cl.torrents {
		fmt.Fprintf(w, "%s: %f%%\n", t.Name(), func() float32 {
			if !t.haveInfo() {
				return 0
			} else {
				return 100 * (1 - float32(t.BytesLeft())/float32(t.Length()))
			}
		}())
		t.WriteStatus(w)
	}
}

// Read torrent data at the given offset. Returns ErrDataNotReady if the data
// isn't available.
func (cl *Client) TorrentReadAt(ih InfoHash, off int64, p []byte) (n int, err error) {
	cl.mu.Lock()
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
	t.lastReadPiece = int(index)
	piece := t.Pieces[index]
	pieceOff := pp.Integer(off % int64(t.PieceLength(0)))
	high := int(t.PieceLength(index) - pieceOff)
	if high < len(p) {
		p = p[:high]
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
	return t.Data.ReadAt(p, off)
}

// Starts the client. Defaults are applied. The client will begin accepting
// connections and tracking.
func (c *Client) Start() {
	c.event.L = &c.mu
	c.torrents = make(map[InfoHash]*torrent)
	if c.HalfOpenLimit == 0 {
		c.HalfOpenLimit = 10
	}
	o := copy(c.PeerId[:], BEP20)
	_, err := rand.Read(c.PeerId[o:])
	if err != nil {
		panic("error generating peer id")
	}
	c.quit = make(chan struct{})
	if c.DownloadStrategy == nil {
		c.DownloadStrategy = &DefaultDownloadStrategy{}
	}
	if c.Listener != nil {
		go c.acceptConnections()
	}
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
	me.event.Broadcast()
	for _, t := range me.torrents {
		for _, c := range t.Conns {
			c.Close()
		}
	}
	me.mu.Unlock()
}

func (cl *Client) acceptConnections() {
	for {
		conn, err := cl.Listener.Accept()
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
		// log.Printf("accepted connection from %s", conn.RemoteAddr())
		go func() {
			if err := cl.runConnection(conn, nil, peerSourceIncoming); err != nil {
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

func (me *Client) initiateConn(peer Peer, torrent *torrent) {
	if peer.Id == me.PeerId {
		return
	}
	me.halfOpen++
	go func() {
		addr := &net.TCPAddr{
			IP:   peer.IP,
			Port: peer.Port,
		}
		conn, err := net.DialTimeout(addr.Network(), addr.String(), dialTimeout)

		go func() {
			me.mu.Lock()
			defer me.mu.Unlock()
			if me.halfOpen == 0 {
				panic("assert")
			}
			me.halfOpen--
			me.openNewConns()
		}()

		if netOpErr, ok := err.(*net.OpError); ok {
			if netOpErr.Timeout() {
				return
			}
			switch netOpErr.Err {
			case syscall.ECONNREFUSED, syscall.EHOSTUNREACH:
				return
			}
		}
		if err != nil {
			log.Printf("error connecting to peer: %s %#v", err, err)
			return
		}
		// log.Printf("connected to %s", conn.RemoteAddr())
		err = me.runConnection(conn, torrent, peer.Source)
		if err != nil {
			log.Print(err)
		}
	}()
}

func (cl *Client) incomingPeerPort() int {
	if cl.Listener == nil {
		return 0
	}
	_, p, err := net.SplitHostPort(cl.Listener.Addr().String())
	if err != nil {
		panic(err)
	}
	var i int
	_, err = fmt.Sscanf(p, "%d", &i)
	if err != nil {
		panic(err)
	}
	return i
}

func (me *Client) runConnection(sock net.Conn, torrent *torrent, discovery peerSource) (err error) {
	conn := &connection{
		Discovery:       discovery,
		Socket:          sock,
		Choked:          true,
		PeerChoked:      true,
		write:           make(chan []byte),
		post:            make(chan pp.Message),
		PeerMaxRequests: 250, // Default in libtorrent is 250.
	}
	defer func() {
		// There's a lock and deferred unlock later in this function. The
		// client will not be locked when this deferred is invoked.
		me.mu.Lock()
		defer me.mu.Unlock()
		conn.Close()
	}()
	go conn.writer()
	// go conn.writeOptimizer()
	conn.write <- pp.Bytes(pp.Protocol)
	conn.write <- pp.Bytes("\x00\x00\x00\x00\x00\x10\x00\x00")
	if torrent != nil {
		conn.write <- pp.Bytes(torrent.InfoHash[:])
		conn.write <- pp.Bytes(me.PeerId[:])
	}
	var b [28]byte
	_, err = io.ReadFull(conn.Socket, b[:])
	if err == io.EOF {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("when reading protocol and extensions: %s", err)
		return
	}
	if string(b[:20]) != pp.Protocol {
		err = fmt.Errorf("wrong protocol: %#v", string(b[:20]))
		return
	}
	if 8 != copy(conn.PeerExtensions[:], b[20:]) {
		panic("wtf")
	}
	// log.Printf("peer extensions: %#v", string(conn.PeerExtensions[:]))
	var infoHash [20]byte
	_, err = io.ReadFull(conn.Socket, infoHash[:])
	if err != nil {
		return fmt.Errorf("reading peer info hash: %s", err)
	}
	_, err = io.ReadFull(conn.Socket, conn.PeerId[:])
	if err != nil {
		return fmt.Errorf("reading peer id: %s", err)
	}
	if torrent == nil {
		torrent = me.torrent(infoHash)
		if torrent == nil {
			return
		}
		conn.write <- pp.Bytes(torrent.InfoHash[:])
		conn.write <- pp.Bytes(me.PeerId[:])
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	if !me.addConnection(torrent, conn) {
		return
	}
	go conn.writeOptimizer(time.Minute)
	if conn.PeerExtensions[5]&0x10 != 0 {
		conn.Post(pp.Message{
			Type:       pp.Extended,
			ExtendedID: pp.HandshakeExtendedID,
			ExtendedPayload: func() []byte {
				d := map[string]interface{}{
					"m": map[string]int{
						"ut_metadata": 1,
						"ut_pex":      2,
					},
					"v": "go.torrent dev",
				}
				if torrent.metadataSizeKnown() {
					d["metadata_size"] = torrent.metadataSize()
				}
				if p := me.incomingPeerPort(); p != 0 {
					d["p"] = p
				}
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
	err = me.connectionLoop(torrent, conn)
	if err != nil {
		err = fmt.Errorf("during Connection loop: %s", err)
	}
	me.dropConnection(torrent, conn)
	return
}

func (me *Client) peerGotPiece(t *torrent, c *connection, piece int) {
	for piece >= len(c.PeerPieces) {
		c.PeerPieces = append(c.PeerPieces, false)
	}
	c.PeerPieces[piece] = true
	if t.wantPiece(piece) {
		me.replenishConnRequests(t, c)
	}
}

func (me *Client) peerUnchoked(torrent *torrent, conn *connection) {
	me.replenishConnRequests(torrent, conn)
}

func (cl *Client) connCancel(t *torrent, cn *connection, r request) (ok bool) {
	ok = cn.Cancel(r)
	if ok {
		cl.DownloadStrategy.DeleteRequest(t, r)
	}
	return
}

func (cl *Client) connDeleteRequest(t *torrent, cn *connection, r request) {
	if !cn.RequestPending(r) {
		return
	}
	cl.DownloadStrategy.DeleteRequest(t, r)
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
	copy(ih[:], h.Sum(nil)[:])
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
		t.SaveMetadataPiece(piece, payload[len(payload)-metadataPieceSize(d["total_size"], piece):])
		if !t.HaveAllMetadataPieces() {
			break
		}
		cl.completedMetadata(t)
	case pp.RequestMetadataExtensionMsgType:
		if !t.HaveMetadataPiece(piece) {
			c.Post(t.NewMetadataExtensionMessage(c, pp.RejectMetadataExtensionMsgType, d["piece"], nil))
			break
		}
		c.Post(t.NewMetadataExtensionMessage(c, pp.DataMetadataExtensionMsgType, piece, t.MetaData[(1<<14)*piece:(1<<14)*piece+t.metadataPieceSize(piece)]))
	case pp.RejectMetadataExtensionMsgType:
	default:
		err = errors.New("unknown msg_type value")
	}
	return
}

type peerExchangeMessage struct {
	Added      util.CompactPeers `bencode:"added"`
	AddedFlags []byte            `bencode:"added.f"`
	Dropped    []tracker.Peer    `bencode:"dropped"`
}

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
		if c.closed {
			return nil
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
			c.Unchoke()
		case pp.NotInterested:
			c.PeerInterested = false
			c.Choke()
		case pp.Have:
			me.peerGotPiece(t, c, int(msg.Index))
		case pp.Request:
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
		case pp.Cancel:
			req := newRequest(msg.Index, msg.Begin, msg.Length)
			if !c.PeerCancel(req) {
				log.Printf("received unexpected cancel: %v", req)
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
					log.Printf("added %d peers from PEX", len(pexMsg.Added))
				}()
			default:
				err = fmt.Errorf("unexpected extended message ID: %v", msg.ExtendedID)
			}
		default:
			err = fmt.Errorf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			return err
		}
	}
}

func (me *Client) dropConnection(torrent *torrent, conn *connection) {
	conn.Socket.Close()
	for r := range conn.Requests {
		me.connDeleteRequest(torrent, conn, r)
	}
	for i0, c := range torrent.Conns {
		if c != conn {
			continue
		}
		i1 := len(torrent.Conns) - 1
		if i0 != i1 {
			torrent.Conns[i0] = torrent.Conns[i1]
		}
		torrent.Conns = torrent.Conns[:i1]
		return
	}
	panic("connection not found")
}

func (me *Client) addConnection(t *torrent, c *connection) bool {
	if me.stopped() {
		return false
	}
	for _, c0 := range t.Conns {
		if c.PeerId == c0.PeerId {
			// Already connected to a client with that ID.
			return false
		}
	}
	t.Conns = append(t.Conns, c)
	return true
}

func (me *Client) openNewConns() {
	for _, t := range me.torrents {
		for len(t.Peers) != 0 {
			if me.halfOpen >= me.HalfOpenLimit {
				return
			}
			p := t.Peers[0]
			t.Peers = t.Peers[1:]
			me.initiateConn(p, t)
		}
	}
}

// Adds peers to the swarm for the torrent corresponding to infoHash.
func (me *Client) AddPeers(infoHash InfoHash, peers []Peer) error {
	me.mu.Lock()
	t := me.torrent(infoHash)
	if t == nil {
		return errors.New("no such torrent")
	}
	t.Peers = append(t.Peers, peers...)
	me.openNewConns()
	me.mu.Unlock()
	return nil
}

func (cl *Client) setMetaData(t *torrent, md metainfo.Info, bytes []byte) (err error) {
	err = t.setMetadata(md, cl.DataDir, bytes)
	if err != nil {
		return
	}
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

	cl.DownloadStrategy.TorrentStarted(t)
	return
}

// Prepare a Torrent without any attachment to a Client. That means we can
// initialize fields all fields that don't require the Client without locking
// it.
func newTorrent(ih InfoHash, announceList [][]string) (t *torrent, err error) {
	t = &torrent{
		InfoHash: ih,
	}
	t.Trackers = make([][]tracker.Client, len(announceList))
	for tierIndex := range announceList {
		tier := t.Trackers[tierIndex]
		for _, url := range announceList[tierIndex] {
			tr, err := tracker.New(url)
			if err != nil {
				log.Print(err)
				continue
			}
			tier = append(tier, tr)
		}
		// The trackers within each tier must be shuffled before use.
		// http://stackoverflow.com/a/12267471/149482
		// http://www.bittorrent.org/beps/bep_0012.html#order-of-processing
		for i := range tier {
			j := mathRand.Intn(i + 1)
			tier[i], tier[j] = tier[j], tier[i]
		}
		t.Trackers[tierIndex] = tier
	}
	return
}

func (cl *Client) AddMagnet(uri string) (err error) {
	m, err := ParseMagnetURI(uri)
	if err != nil {
		return
	}
	t, err := newTorrent(m.InfoHash, [][]string{m.Trackers})
	if err != nil {
		return
	}
	t.DisplayName = m.DisplayName
	cl.mu.Lock()
	defer cl.mu.Unlock()
	err = cl.addTorrent(t)
	if err != nil {
		t.Close()
	}
	return
}

func (me *Client) addTorrent(t *torrent) (err error) {
	if _, ok := me.torrents[t.InfoHash]; ok {
		err = fmt.Errorf("torrent infohash collision")
		return
	}
	me.torrents[t.InfoHash] = t
	if !me.DisableTrackers {
		go me.announceTorrent(t)
	}
	if me.DHT != nil {
		go me.announceTorrentDHT(t)
	}
	return
}

// Adds the torrent to the client.
func (me *Client) AddTorrent(metaInfo *metainfo.MetaInfo) (err error) {
	t, err := newTorrent(BytesInfoHash(metaInfo.Info.Hash), metaInfo.AnnounceList)
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

func (cl *Client) listenerAnnouncePort() (port int16) {
	l := cl.Listener
	if l == nil {
		return
	}
	addr := l.Addr()
	switch data := addr.(type) {
	case *net.TCPAddr:
		return int16(data.Port)
	case *net.UDPAddr:
		return int16(data.Port)
	default:
		log.Printf("unknown listener addr type: %T", addr)
	}
	return
}

func (cl *Client) announceTorrentDHT(t *torrent) {
	for {
		ps, err := cl.DHT.GetPeers(string(t.InfoHash[:]))
		if err != nil {
			log.Printf("error getting peers from dht: %s", err)
			return
		}
		nextScrape := time.After(1 * time.Minute)
	getPeers:
		for {
			select {
			case <-nextScrape:
				break getPeers
			case cps, ok := <-ps.Values:
				if !ok {
					break getPeers
				}
				err = cl.AddPeers(t.InfoHash, func() (ret []Peer) {
					for _, cp := range cps {
						ret = append(ret, Peer{
							IP:     cp.IP[:],
							Port:   int(cp.Port),
							Source: peerSourceDHT,
						})
						// log.Printf("peer from dht: %s", &net.UDPAddr{
						// 	IP:   cp.IP[:],
						// 	Port: int(cp.Port),
						// })
					}
					return
				}())
				if err != nil {
					log.Printf("error adding peers from dht for torrent %q: %s", t, err)
					break getPeers
				}
				log.Printf("got %d peers from dht for torrent %q", len(cps), t)
			}
		}
		ps.Close()
	}
}

func (cl *Client) announceTorrent(t *torrent) {
	req := tracker.AnnounceRequest{
		Event:    tracker.Started,
		NumWant:  -1,
		Port:     cl.listenerAnnouncePort(),
		PeerId:   cl.PeerId,
		InfoHash: t.InfoHash,
	}
newAnnounce:
	for {
		cl.mu.Lock()
		req.Left = t.BytesLeft()
		cl.mu.Unlock()
		for _, tier := range t.Trackers {
			for trIndex, tr := range tier {
				if err := tr.Connect(); err != nil {
					log.Print(err)
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
					log.Print(err)
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
		if !t.haveAllPieces() {
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
	dds, ok := cl.DownloadStrategy.(*DefaultDownloadStrategy)
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
	me.DownloadStrategy.FillRequests(t, c)
	//me.assertRequestHeat()
	if len(c.Requests) == 0 && !c.PeerChoked {
		c.SetInterested(false)
	}
}

func (me *Client) downloadedChunk(t *torrent, c *connection, msg *pp.Message) error {
	req := newRequest(msg.Index, msg.Begin, pp.Integer(len(msg.Piece)))

	// Request has been satisfied.
	me.connDeleteRequest(t, c, req)

	defer me.replenishConnRequests(t, c)

	// Do we actually want this chunk?
	if _, ok := t.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
		log.Printf("got unnecessary chunk from %v: %q", req, string(c.PeerId[:]))
		return nil
	}

	// Write the chunk out.
	err := t.WriteChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	if err != nil {
		return err
	}

	// Record that we have the chunk.
	delete(t.Pieces[req.Index].PendingChunkSpecs, req.chunkSpec)
	t.PiecesByBytesLeft.ValueChanged(t.Pieces[req.Index].bytesLeftElement)
	if len(t.Pieces[req.Index].PendingChunkSpecs) == 0 {
		me.queuePieceCheck(t, req.Index)
	}

	// Unprioritize the chunk.
	var next *list.Element
	for e := t.Priorities.Front(); e != nil; e = next {
		next = e.Next()
		if e.Value.(request) == req {
			t.Priorities.Remove(e)
		}
	}

	// Cancel pending requests for this chunk.
	cancelled := false
	for _, c := range t.Conns {
		if me.connCancel(t, c, req) {
			cancelled = true
			me.replenishConnRequests(t, c)
		}
	}
	if cancelled {
		log.Printf("cancelled concurrent requests for %v", req)
	}

	me.dataReady(dataSpec{t.InfoHash, req})
	return nil
}

func (cl *Client) dataReady(ds dataSpec) {
	if cl.dataWaiter != nil {
		close(cl.dataWaiter)
	}
	cl.dataWaiter = nil
}

// Returns a channel that is closed when new data has become available in the
// client.
func (me *Client) DataWaiter() <-chan struct{} {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.dataWaiter == nil {
		me.dataWaiter = make(chan struct{})
	}
	return me.dataWaiter
}

func (me *Client) pieceHashed(t *torrent, piece pp.Integer, correct bool) {
	p := t.Pieces[piece]
	p.EverHashed = true
	if correct {
		p.PendingChunkSpecs = nil
		// log.Printf("%s: got piece %d, (%d/%d)", t, piece, t.NumPiecesCompleted(), t.NumPieces())
		var next *list.Element
		for e := t.Priorities.Front(); e != nil; e = next {
			next = e.Next()
			if e.Value.(request).Index == piece {
				t.Priorities.Remove(e)
			}
		}
		me.dataReady(dataSpec{
			t.InfoHash,
			request{
				pp.Integer(piece),
				chunkSpec{0, pp.Integer(t.PieceLength(piece))},
			},
		})
	} else {
		if len(p.PendingChunkSpecs) == 0 {
			t.pendAllChunkSpecs(piece)
		}
	}
	for _, conn := range t.Conns {
		if correct {
			conn.Post(pp.Message{
				Type:  pp.Have,
				Index: pp.Integer(piece),
			})
			// TODO: Cancel requests for this piece.
		} else {
			if conn.PeerHasPiece(piece) {
				me.replenishConnRequests(t, conn)
			}
		}
	}
	me.event.Broadcast()
}

func (cl *Client) verifyPiece(t *torrent, index pp.Integer) {
	cl.mu.Lock()
	p := t.Pieces[index]
	for p.Hashing {
		cl.event.Wait()
	}
	p.Hashing = true
	p.QueuedForHash = false
	cl.mu.Unlock()
	sum := t.HashPiece(index)
	cl.mu.Lock()
	p.Hashing = false
	cl.pieceHashed(t, index, sum == p.Hash)
	cl.mu.Unlock()
}

func (me *Client) Torrents() (ret []*torrent) {
	me.mu.Lock()
	for _, t := range me.torrents {
		ret = append(ret, t)
	}
	me.mu.Unlock()
	return
}
