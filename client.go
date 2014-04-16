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
	"container/list"
	"crypto/rand"
	"encoding"
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

	metainfo "github.com/nsf/libtorgo/torrent"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
)

// Currently doesn't really queue, but should in the future.
func (cl *Client) queuePieceCheck(t *torrent, pieceIndex peer_protocol.Integer) {
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
	if t.Priorities == nil {
		t.Priorities = list.New()
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
	DataDir         string
	HalfOpenLimit   int
	PeerId          [20]byte
	Listener        net.Listener
	DisableTrackers bool

	mu    sync.Mutex
	event sync.Cond
	quit  chan struct{}

	halfOpen   int
	torrents   map[InfoHash]*torrent
	dataWaiter chan struct{}
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
	index := peer_protocol.Integer(off / t.MetaInfo.PieceLength)
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
	if !piece.EverHashed {
		cl.queuePieceCheck(t, index)
	}
	if piece.Hashing {
		err = ErrDataNotReady
		return
	}
	pieceOff := peer_protocol.Integer(off % int64(t.PieceLength(0)))
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

// Starts the client. Defaults are applied. The client will begin accepting connections and tracking.
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
			return
		default:
		}
		if err != nil {
			log.Print(err)
			return
		}
		go func() {
			if err := cl.runConnection(conn, nil); err != nil {
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
		log.Printf("connected to %s", conn.RemoteAddr())
		err = me.runConnection(conn, torrent)
		if err != nil {
			log.Print(err)
		}
	}()
}

func (me *Client) runConnection(sock net.Conn, torrent *torrent) (err error) {
	conn := &connection{
		Socket:     sock,
		Choked:     true,
		PeerChoked: true,
		write:      make(chan []byte),
		post:       make(chan encoding.BinaryMarshaler),
	}
	defer func() {
		// There's a lock and deferred unlock later in this function. The
		// client will not be locked when this deferred is invoked.
		me.mu.Lock()
		defer me.mu.Unlock()
		conn.Close()
	}()
	go conn.writer()
	go conn.writeOptimizer()
	conn.post <- peer_protocol.Bytes(peer_protocol.Protocol)
	conn.post <- peer_protocol.Bytes("\x00\x00\x00\x00\x00\x00\x00\x00")
	if torrent != nil {
		conn.post <- peer_protocol.Bytes(torrent.InfoHash[:])
		conn.post <- peer_protocol.Bytes(me.PeerId[:])
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
	if string(b[:20]) != peer_protocol.Protocol {
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
		conn.post <- peer_protocol.Bytes(torrent.InfoHash[:])
		conn.post <- peer_protocol.Bytes(me.PeerId[:])
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	if !me.addConnection(torrent, conn) {
		return
	}
	if torrent.haveAnyPieces() {
		conn.Post(peer_protocol.Message{
			Type:     peer_protocol.Bitfield,
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

func (me *Client) peerGotPiece(torrent *torrent, conn *connection, piece int) {
	if conn.PeerPieces == nil {
		conn.PeerPieces = make([]bool, len(torrent.Pieces))
	}
	conn.PeerPieces[piece] = true
	if torrent.wantPiece(piece) {
		me.replenishConnRequests(torrent, conn)
	}
}

func (me *Client) peerUnchoked(torrent *torrent, conn *connection) {
	me.replenishConnRequests(torrent, conn)
}

func (me *Client) connectionLoop(torrent *torrent, conn *connection) error {
	decoder := peer_protocol.Decoder{
		R:         bufio.NewReader(conn.Socket),
		MaxLength: 256 * 1024,
	}
	for {
		me.mu.Unlock()
		// TODO: Can this be allocated on the stack?
		msg := new(peer_protocol.Message)
		err := decoder.Decode(msg)
		me.mu.Lock()
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
		case peer_protocol.Choke:
			conn.PeerChoked = true
			conn.Requests = nil
		case peer_protocol.Unchoke:
			conn.PeerChoked = false
			me.peerUnchoked(torrent, conn)
		case peer_protocol.Interested:
			conn.PeerInterested = true
			// TODO: This should be done from a dedicated unchoking routine.
			conn.Unchoke()
		case peer_protocol.NotInterested:
			conn.PeerInterested = false
		case peer_protocol.Have:
			me.peerGotPiece(torrent, conn, int(msg.Index))
		case peer_protocol.Request:
			if conn.PeerRequests == nil {
				conn.PeerRequests = make(map[request]struct{}, maxRequests)
			}
			request := request{
				Index:     msg.Index,
				chunkSpec: chunkSpec{msg.Begin, msg.Length},
			}
			conn.PeerRequests[request] = struct{}{}
			// TODO: Requests should be satisfied from a dedicated upload routine.
			p := make([]byte, msg.Length)
			n, err := torrent.Data.ReadAt(p, int64(torrent.PieceLength(0))*int64(msg.Index)+int64(msg.Begin))
			if err != nil {
				return fmt.Errorf("reading torrent data to serve request %s: %s", request, err)
			}
			if n != int(msg.Length) {
				return fmt.Errorf("bad request: %s", msg)
			}
			conn.Post(peer_protocol.Message{
				Type:  peer_protocol.Piece,
				Index: msg.Index,
				Begin: msg.Begin,
				Piece: p,
			})
		case peer_protocol.Cancel:
			req := newRequest(msg.Index, msg.Begin, msg.Length)
			if !conn.PeerCancel(req) {
				log.Printf("received unexpected cancel: %v", req)
			}
		case peer_protocol.Bitfield:
			if len(msg.Bitfield) < len(torrent.Pieces) {
				err = errors.New("received invalid bitfield")
				break
			}
			if conn.PeerPieces != nil {
				err = errors.New("received unexpected bitfield")
				break
			}
			conn.PeerPieces = msg.Bitfield[:len(torrent.Pieces)]
			for index, has := range conn.PeerPieces {
				if has {
					me.peerGotPiece(torrent, conn, index)
				}
			}
		case peer_protocol.Piece:
			request_ := request{msg.Index, chunkSpec{msg.Begin, peer_protocol.Integer(len(msg.Piece))}}
			if _, ok := conn.Requests[request_]; !ok {
				err = fmt.Errorf("unexpected piece: %s", request_)
				break
			}
			delete(conn.Requests, request_)
			err = me.downloadedChunk(torrent, msg)
		default:
			log.Printf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			return err
		}
		me.replenishConnRequests(torrent, conn)
	}
}

func (me *Client) dropConnection(torrent *torrent, conn *connection) {
	conn.Socket.Close()
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
	panic("no such Connection")
}

func (me *Client) addConnection(t *torrent, c *connection) bool {
	for _, c0 := range t.Conns {
		if c.PeerId == c0.PeerId {
			log.Printf("%s and %s have the same ID: %s", c.Socket.RemoteAddr(), c0.Socket.RemoteAddr(), c.PeerId)
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

// Prepare a Torrent without any attachment to a Client. That means we can
// initialize fields all fields that don't require the Client without locking
// it.
func newTorrent(metaInfo *metainfo.MetaInfo, dataDir string) (t *torrent, err error) {
	t = &torrent{
		InfoHash: BytesInfoHash(metaInfo.InfoHash),
		MetaInfo: metaInfo,
	}
	t.Data, err = mmapTorrentData(metaInfo, dataDir)
	if err != nil {
		return
	}
	for offset := 0; offset < len(metaInfo.Pieces); offset += pieceHash.Size() {
		hash := metaInfo.Pieces[offset : offset+pieceHash.Size()]
		if len(hash) != pieceHash.Size() {
			err = errors.New("bad piece hash in metainfo")
			return
		}
		piece := &piece{}
		copyHashSum(piece.Hash[:], hash)
		t.Pieces = append(t.Pieces, piece)
		t.pendAllChunkSpecs(peer_protocol.Integer(len(t.Pieces) - 1))
	}
	t.Trackers = make([][]tracker.Client, len(metaInfo.AnnounceList))
	for tierIndex := range metaInfo.AnnounceList {
		tier := t.Trackers[tierIndex]
		for _, url := range metaInfo.AnnounceList[tierIndex] {
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

// Adds the torrent to the client.
func (me *Client) AddTorrent(metaInfo *metainfo.MetaInfo) error {
	torrent, err := newTorrent(metaInfo, me.DataDir)
	if err != nil {
		return err
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	if _, ok := me.torrents[torrent.InfoHash]; ok {
		return torrent.Close()
	}
	me.torrents[torrent.InfoHash] = torrent
	if !me.DisableTrackers {
		go me.announceTorrent(torrent)
	}
	for i := range torrent.Pieces {
		me.queuePieceCheck(torrent, peer_protocol.Integer(i))
	}
	return nil
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

func (cl *Client) announceTorrent(t *torrent) {
	req := tracker.AnnounceRequest{
		Event:   tracker.Started,
		NumWant: -1,
		Port:    cl.listenerAnnouncePort(),
	}
	req.PeerId = cl.PeerId
	req.InfoHash = t.InfoHash
newAnnounce:
	for {
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
				if err := cl.AddPeers(t.InfoHash, peers); err != nil {
					log.Print(err)
					return
				}
				log.Printf("%d new peers from %s", len(peers), "TODO")
				tier[0], tier[trIndex] = tier[trIndex], tier[0]
				time.Sleep(time.Second * time.Duration(resp.Interval))
				continue newAnnounce
			}
		}
		time.Sleep(time.Second)
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
// client is stopped.
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

func (me *Client) replenishConnRequests(torrent *torrent, conn *connection) {
	requestHeatMap := torrent.requestHeat()
	addRequest := func(req request) (again bool) {
		piece := torrent.Pieces[req.Index]
		if piece.Hashing {
			// We can't be sure we want this.
			return true
		}
		if piece.Complete() {
			// We already have this.
			return true
		}
		if requestHeatMap[req] > 0 {
			// We've already requested this.
			return true
		}
		return conn.Request(req)
	}
	// First request prioritized chunks.
	if torrent.Priorities != nil {
		for e := torrent.Priorities.Front(); e != nil; e = e.Next() {
			if !addRequest(e.Value.(request)) {
				return
			}
		}
	}
	// Then finish off incomplete pieces in order of bytes remaining.
	for _, index := range torrent.piecesByPendingBytesDesc() {
		if torrent.PieceNumPendingBytes(index) == torrent.PieceLength(index) {
			continue
		}
		for chunkSpec := range torrent.Pieces[index].PendingChunkSpecs {
			if !addRequest(request{index, chunkSpec}) {
				return
			}
		}
	}
	if len(conn.Requests) == 0 {
		conn.SetInterested(false)
	}
}

func (me *Client) downloadedChunk(torrent *torrent, msg *peer_protocol.Message) (err error) {
	req := request{msg.Index, chunkSpec{msg.Begin, peer_protocol.Integer(len(msg.Piece))}}
	if _, ok := torrent.Pieces[req.Index].PendingChunkSpecs[req.chunkSpec]; !ok {
		log.Printf("got unnecessary chunk: %s", req)
		return
	}
	err = torrent.WriteChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	if err != nil {
		return
	}
	delete(torrent.Pieces[req.Index].PendingChunkSpecs, req.chunkSpec)
	if len(torrent.Pieces[req.Index].PendingChunkSpecs) == 0 {
		me.queuePieceCheck(torrent, req.Index)
	}
	var next *list.Element
	for e := torrent.Priorities.Front(); e != nil; e = next {
		next = e.Next()
		if e.Value.(request) == req {
			torrent.Priorities.Remove(e)
		}
	}
	me.dataReady(dataSpec{torrent.InfoHash, req})
	return
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

func (me *Client) pieceHashed(t *torrent, piece peer_protocol.Integer, correct bool) {
	p := t.Pieces[piece]
	p.EverHashed = true
	if correct {
		p.PendingChunkSpecs = nil
		log.Printf("got piece %d, (%d/%d)", piece, t.NumPiecesCompleted(), t.NumPieces())
		var next *list.Element
		if t.Priorities != nil {
			for e := t.Priorities.Front(); e != nil; e = next {
				next = e.Next()
				if e.Value.(request).Index == piece {
					t.Priorities.Remove(e)
				}
			}
		}
		me.dataReady(dataSpec{
			t.InfoHash,
			request{
				peer_protocol.Integer(piece),
				chunkSpec{0, peer_protocol.Integer(t.PieceLength(piece))},
			},
		})
	} else {
		if len(p.PendingChunkSpecs) == 0 {
			t.pendAllChunkSpecs(piece)
		}
	}
	for _, conn := range t.Conns {
		if correct {
			conn.Post(peer_protocol.Message{
				Type:  peer_protocol.Have,
				Index: peer_protocol.Integer(piece),
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

func (cl *Client) verifyPiece(t *torrent, index peer_protocol.Integer) {
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
