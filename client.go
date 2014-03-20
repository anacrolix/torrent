package torrent

import (
	"bufio"
	"container/list"
	"crypto"
	"crypto/rand"
	"encoding"
	"errors"
	"fmt"
	"io"
	"log"
	mathRand "math/rand"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	metainfo "github.com/nsf/libtorgo/torrent"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
	"launchpad.net/gommap"
)

const (
	PieceHash   = crypto.SHA1
	maxRequests = 250
	chunkSize   = 0x4000 // 16KiB
	BEP20       = "-GT0000-"
)

type InfoHash [20]byte

type pieceSum [20]byte

func copyHashSum(dst, src []byte) {
	if len(dst) != len(src) || copy(dst, src) != len(dst) {
		panic("hash sum sizes differ")
	}
}

func BytesInfoHash(b []byte) (ih InfoHash) {
	if len(b) != len(ih) || copy(ih[:], b) != len(ih) {
		panic("bad infohash bytes")
	}
	return
}

type piece struct {
	Hash              pieceSum
	PendingChunkSpecs map[ChunkSpec]struct{}
	Hashing           bool
	QueuedForHash     bool
	EverHashed        bool
}

func (p *piece) Complete() bool {
	return len(p.PendingChunkSpecs) == 0 && p.EverHashed
}

func lastChunkSpec(pieceLength peer_protocol.Integer) (cs ChunkSpec) {
	cs.Begin = (pieceLength - 1) / chunkSize * chunkSize
	cs.Length = pieceLength - cs.Begin
	return
}

func (t *Torrent) PieceNumPendingBytes(index peer_protocol.Integer) (count peer_protocol.Integer) {
	pendingChunks := t.Pieces[index].PendingChunkSpecs
	count = peer_protocol.Integer(len(pendingChunks)) * chunkSize
	_lastChunkSpec := lastChunkSpec(t.PieceLength(index))
	if _lastChunkSpec.Length != chunkSize {
		if _, ok := pendingChunks[_lastChunkSpec]; ok {
			count += _lastChunkSpec.Length - chunkSize
		}
	}
	return
}

type ChunkSpec struct {
	Begin, Length peer_protocol.Integer
}

type Request struct {
	Index peer_protocol.Integer
	ChunkSpec
}

type Connection struct {
	Socket net.Conn
	Closed bool
	post   chan encoding.BinaryMarshaler
	write  chan []byte

	// Stuff controlled by the local peer.
	Interested bool
	Choked     bool
	Requests   map[Request]struct{}

	// Stuff controlled by the remote peer.
	PeerId         [20]byte
	PeerInterested bool
	PeerChoked     bool
	PeerRequests   map[Request]struct{}
	PeerExtensions [8]byte
	PeerPieces     []bool
}

func (c *Connection) Close() {
	if c.Closed {
		return
	}
	c.Socket.Close()
	close(c.post)
	c.Closed = true
}

func (c *Connection) PeerHasPiece(index peer_protocol.Integer) bool {
	if c.PeerPieces == nil {
		return false
	}
	return c.PeerPieces[index]
}

func (c *Connection) Post(msg encoding.BinaryMarshaler) {
	c.post <- msg
}

// Returns true if more requests can be sent.
func (c *Connection) Request(chunk Request) bool {
	if len(c.Requests) >= maxRequests {
		return false
	}
	if !c.PeerPieces[chunk.Index] {
		return true
	}
	c.SetInterested(true)
	if c.PeerChoked {
		return false
	}
	if _, ok := c.Requests[chunk]; !ok {
		c.Post(peer_protocol.Message{
			Type:   peer_protocol.Request,
			Index:  chunk.Index,
			Begin:  chunk.Begin,
			Length: chunk.Length,
		})
	}
	if c.Requests == nil {
		c.Requests = make(map[Request]struct{}, maxRequests)
	}
	c.Requests[chunk] = struct{}{}
	return true
}

func (c *Connection) Unchoke() {
	if !c.Choked {
		return
	}
	c.Post(peer_protocol.Message{
		Type: peer_protocol.Unchoke,
	})
	c.Choked = false
}

func (c *Connection) SetInterested(interested bool) {
	if c.Interested == interested {
		return
	}
	c.Post(peer_protocol.Message{
		Type: func() peer_protocol.MessageType {
			if interested {
				return peer_protocol.Interested
			} else {
				return peer_protocol.NotInterested
			}
		}(),
	})
	c.Interested = interested
}

var (
	keepAliveBytes [4]byte
)

func (conn *Connection) writer() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		if !timer.Reset(time.Minute) {
			<-timer.C
		}
		var b []byte
		select {
		case <-timer.C:
			b = keepAliveBytes[:]
		case b = <-conn.write:
			if b == nil {
				return
			}
		}
		n, err := conn.Socket.Write(b)
		if err != nil {
			log.Print(err)
			break
		}
		if n != len(b) {
			panic("didn't write all bytes")
		}
	}
}

func (conn *Connection) writeOptimizer() {
	pending := list.New()
	var nextWrite []byte
	defer close(conn.write)
	for {
		write := conn.write
		if pending.Len() == 0 {
			write = nil
		} else {
			var err error
			nextWrite, err = pending.Front().Value.(encoding.BinaryMarshaler).MarshalBinary()
			if err != nil {
				panic(err)
			}
		}
		select {
		case msg, ok := <-conn.post:
			if !ok {
				return
			}
			pending.PushBack(msg)
		case write <- nextWrite:
			pending.Remove(pending.Front())
		}
	}
}

type Torrent struct {
	InfoHash   InfoHash
	Pieces     []*piece
	Data       MMapSpan
	MetaInfo   *metainfo.MetaInfo
	Conns      []*Connection
	Peers      []Peer
	Priorities *list.List
	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list key.
	Trackers [][]tracker.Client
}

func (t *Torrent) NumPieces() int {
	return len(t.MetaInfo.Pieces) / PieceHash.Size()
}

func (t *Torrent) NumPiecesCompleted() (num int) {
	for _, p := range t.Pieces {
		if p.Complete() {
			num++
		}
	}
	return
}

func (t *Torrent) Length() int64 {
	return int64(t.PieceLength(peer_protocol.Integer(len(t.Pieces)-1))) + int64(len(t.Pieces)-1)*int64(t.PieceLength(0))
}

func (t *Torrent) Close() (err error) {
	t.Data.Close()
	for _, conn := range t.Conns {
		conn.Close()
	}
	return
}

type pieceByBytesPendingSlice struct {
	Pending, Indices []peer_protocol.Integer
}

func (pcs pieceByBytesPendingSlice) Len() int {
	return len(pcs.Indices)
}

func (me pieceByBytesPendingSlice) Less(i, j int) bool {
	return me.Pending[me.Indices[i]] < me.Pending[me.Indices[j]]
}

func (me pieceByBytesPendingSlice) Swap(i, j int) {
	me.Indices[i], me.Indices[j] = me.Indices[j], me.Indices[i]
}

func (t *Torrent) piecesByPendingBytesDesc() (indices []peer_protocol.Integer) {
	slice := pieceByBytesPendingSlice{
		Pending: make([]peer_protocol.Integer, 0, len(t.Pieces)),
		Indices: make([]peer_protocol.Integer, 0, len(t.Pieces)),
	}
	for i := range t.Pieces {
		slice.Pending = append(slice.Pending, t.PieceNumPendingBytes(peer_protocol.Integer(i)))
		slice.Indices = append(slice.Indices, peer_protocol.Integer(i))
	}
	sort.Sort(sort.Reverse(slice))
	return slice.Indices
}

// Currently doesn't really queue, but should in the future.
func (cl *Client) queuePieceCheck(t *Torrent, pieceIndex peer_protocol.Integer) {
	piece := t.Pieces[pieceIndex]
	if piece.QueuedForHash {
		return
	}
	piece.QueuedForHash = true
	go cl.verifyPiece(t, pieceIndex)
}

func (t *Torrent) offsetRequest(off int64) (req Request, ok bool) {
	req.Index = peer_protocol.Integer(off / t.MetaInfo.PieceLength)
	if req.Index < 0 || int(req.Index) >= len(t.Pieces) {
		return
	}
	off %= t.MetaInfo.PieceLength
	pieceLeft := t.PieceLength(req.Index) - peer_protocol.Integer(off)
	if pieceLeft <= 0 {
		return
	}
	req.Begin = chunkSize * (peer_protocol.Integer(off) / chunkSize)
	req.Length = chunkSize
	if req.Length > pieceLeft {
		req.Length = pieceLeft
	}
	ok = true
	return
}

func (cl *Client) PrioritizeDataRegion(ih InfoHash, off, len_ int64) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	t := cl.torrent(ih)
	newPriorities := make([]Request, 0, (len_+2*(chunkSize-1))/chunkSize)
	for len_ > 0 {
		// TODO: Write a function to return the Request for a given offset.
		req, ok := t.offsetRequest(off)
		if !ok {
			break
		}
		off += int64(req.Length)
		len_ -= int64(req.Length)
		if _, ok = t.Pieces[req.Index].PendingChunkSpecs[req.ChunkSpec]; !ok {
			continue
		}
		newPriorities = append(newPriorities, req)
	}
	if len(newPriorities) == 0 {
		return
	}
	if t.Priorities == nil {
		t.Priorities = list.New()
	}
	t.Priorities.PushFront(newPriorities[0])
	for _, req := range newPriorities[1:] {
		t.Priorities.PushBack(req)
	}
	for _, cn := range t.Conns {
		cl.replenishConnRequests(t, cn)
	}
}

func (t *Torrent) WriteChunk(piece int, begin int64, data []byte) (err error) {
	_, err = t.Data.WriteAt(data, int64(piece)*t.MetaInfo.PieceLength+begin)
	return
}

func (t *Torrent) bitfield() (bf []bool) {
	for _, p := range t.Pieces {
		bf = append(bf, p.EverHashed && len(p.PendingChunkSpecs) == 0)
	}
	return
}

func (t *Torrent) pendAllChunkSpecs(index peer_protocol.Integer) {
	piece := t.Pieces[index]
	if piece.PendingChunkSpecs == nil {
		piece.PendingChunkSpecs = make(
			map[ChunkSpec]struct{},
			(t.MetaInfo.PieceLength+chunkSize-1)/chunkSize)
	}
	c := ChunkSpec{
		Begin: 0,
	}
	cs := piece.PendingChunkSpecs
	for left := peer_protocol.Integer(t.PieceLength(index)); left != 0; left -= c.Length {
		c.Length = left
		if c.Length > chunkSize {
			c.Length = chunkSize
		}
		cs[c] = struct{}{}
		c.Begin += c.Length
	}
	return
}

func (t *Torrent) requestHeat() (ret map[Request]int) {
	ret = make(map[Request]int)
	for _, conn := range t.Conns {
		for req, _ := range conn.Requests {
			ret[req]++
		}
	}
	return
}

type Peer struct {
	Id   [20]byte
	IP   net.IP
	Port int
}

func (t *Torrent) PieceLength(piece peer_protocol.Integer) (len_ peer_protocol.Integer) {
	if int(piece) == t.NumPieces()-1 {
		len_ = peer_protocol.Integer(t.Data.Size() % t.MetaInfo.PieceLength)
	}
	if len_ == 0 {
		len_ = peer_protocol.Integer(t.MetaInfo.PieceLength)
	}
	return
}

func (t *Torrent) HashPiece(piece peer_protocol.Integer) (ps pieceSum) {
	hash := PieceHash.New()
	n, err := t.Data.WriteSectionTo(hash, int64(piece)*t.MetaInfo.PieceLength, t.MetaInfo.PieceLength)
	if err != nil {
		panic(err)
	}
	if peer_protocol.Integer(n) != t.PieceLength(piece) {
		panic(fmt.Sprintf("hashed wrong number of bytes: expected %d; did %d; piece %d", t.PieceLength(piece), n, piece))
	}
	copyHashSum(ps[:], hash.Sum(nil))
	return
}

type DataSpec struct {
	InfoHash
	Request
}

type Client struct {
	DataDir         string
	HalfOpenLimit   int
	PeerId          [20]byte
	Listener        net.Listener
	DisableTrackers bool

	sync.Mutex
	mu    *sync.Mutex
	event sync.Cond
	quit  chan struct{}

	halfOpen   int
	torrents   map[InfoHash]*Torrent
	dataWaiter chan struct{}
}

var (
	ErrDataNotReady = errors.New("data not ready")
)

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

func (c *Client) Start() {
	c.mu = &c.Mutex
	c.event.L = c.mu
	c.torrents = make(map[InfoHash]*Torrent)
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

func (me *Client) Stop() {
	me.Lock()
	close(me.quit)
	me.event.Broadcast()
	for _, t := range me.torrents {
		for _, c := range t.Conns {
			c.Close()
		}
	}
	me.Unlock()
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

func mmapTorrentData(metaInfo *metainfo.MetaInfo, location string) (mms MMapSpan, err error) {
	defer func() {
		if err != nil {
			mms.Close()
			mms = nil
		}
	}()
	for _, miFile := range metaInfo.Files {
		fileName := filepath.Join(append([]string{location, metaInfo.Name}, miFile.Path...)...)
		err = os.MkdirAll(filepath.Dir(fileName), 0777)
		if err != nil {
			return
		}
		var file *os.File
		file, err = os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0666)
		if err != nil {
			return
		}
		func() {
			defer file.Close()
			var fi os.FileInfo
			fi, err = file.Stat()
			if err != nil {
				return
			}
			if fi.Size() < miFile.Length {
				err = file.Truncate(miFile.Length)
				if err != nil {
					return
				}
			}
			var mMap gommap.MMap
			mMap, err = gommap.MapRegion(file.Fd(), 0, miFile.Length, gommap.PROT_READ|gommap.PROT_WRITE, gommap.MAP_SHARED)
			if err != nil {
				return
			}
			if int64(len(mMap)) != miFile.Length {
				panic("mmap has wrong length")
			}
			mms = append(mms, MMap{mMap})
		}()
		if err != nil {
			return
		}
	}
	return
}

func (me *Client) torrent(ih InfoHash) *Torrent {
	for _, t := range me.torrents {
		if t.InfoHash == ih {
			return t
		}
	}
	return nil
}

func (me *Client) initiateConn(peer Peer, torrent *Torrent) {
	if peer.Id == me.PeerId {
		return
	}
	me.halfOpen++
	go func() {
		conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   peer.IP,
			Port: peer.Port,
		})

		me.mu.Lock()
		me.halfOpen--
		me.openNewConns()
		me.mu.Unlock()

		if err != nil {
			log.Printf("error connecting to peer: %s", err)
			return
		}
		log.Printf("connected to %s", conn.RemoteAddr())
		err = me.runConnection(conn, torrent)
		if err != nil {
			log.Print(err)
		}
	}()
}

func (t *Torrent) haveAllPieces() bool {
	for _, piece := range t.Pieces {
		if !piece.Complete() {
			return false
		}
	}
	return true
}

func (me *Torrent) haveAnyPieces() bool {
	for _, piece := range me.Pieces {
		if piece.Complete() {
			return true
		}
	}
	return false
}

func (me *Client) runConnection(sock net.Conn, torrent *Torrent) (err error) {
	conn := &Connection{
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

func (me *Client) peerGotPiece(torrent *Torrent, conn *Connection, piece int) {
	if conn.PeerPieces == nil {
		conn.PeerPieces = make([]bool, len(torrent.Pieces))
	}
	conn.PeerPieces[piece] = true
	if torrent.wantPiece(piece) {
		me.replenishConnRequests(torrent, conn)
	}
}

func (t *Torrent) wantPiece(index int) bool {
	p := t.Pieces[index]
	return p.EverHashed && len(p.PendingChunkSpecs) != 0
}

func (me *Client) peerUnchoked(torrent *Torrent, conn *Connection) {
	me.replenishConnRequests(torrent, conn)
}

func (me *Client) connectionLoop(torrent *Torrent, conn *Connection) error {
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
				conn.PeerRequests = make(map[Request]struct{}, maxRequests)
			}
			request := Request{
				Index:     msg.Index,
				ChunkSpec: ChunkSpec{msg.Begin, msg.Length},
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
			request_ := Request{msg.Index, ChunkSpec{msg.Begin, peer_protocol.Integer(len(msg.Piece))}}
			if _, ok := conn.Requests[request_]; !ok {
				err = errors.New("unexpected piece")
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

func (me *Client) dropConnection(torrent *Torrent, conn *Connection) {
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

func (me *Client) addConnection(t *Torrent, c *Connection) bool {
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
func newTorrent(metaInfo *metainfo.MetaInfo, dataDir string) (torrent *Torrent, err error) {
	torrent = &Torrent{
		InfoHash: BytesInfoHash(metaInfo.InfoHash),
		MetaInfo: metaInfo,
	}
	torrent.Data, err = mmapTorrentData(metaInfo, dataDir)
	if err != nil {
		return
	}
	for offset := 0; offset < len(metaInfo.Pieces); offset += PieceHash.Size() {
		hash := metaInfo.Pieces[offset : offset+PieceHash.Size()]
		if len(hash) != PieceHash.Size() {
			err = errors.New("bad piece hash in metainfo")
			return
		}
		piece := &piece{}
		copyHashSum(piece.Hash[:], hash)
		torrent.Pieces = append(torrent.Pieces, piece)
		torrent.pendAllChunkSpecs(peer_protocol.Integer(len(torrent.Pieces) - 1))
	}
	torrent.Trackers = make([][]tracker.Client, len(metaInfo.AnnounceList))
	for tierIndex := range metaInfo.AnnounceList {
		tier := torrent.Trackers[tierIndex]
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
		torrent.Trackers[tierIndex] = tier
	}
	return
}

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

func (cl *Client) announceTorrent(t *Torrent) {
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

func (me *Client) WaitAll() {
	me.mu.Lock()
	for !me.allTorrentsCompleted() {
		me.event.Wait()
	}
	me.mu.Unlock()
}

func (me *Client) replenishConnRequests(torrent *Torrent, conn *Connection) {
	requestHeatMap := torrent.requestHeat()
	addRequest := func(req Request) (again bool) {
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
			if !addRequest(e.Value.(Request)) {
				return
			}
		}
	}
	// Then finish of incomplete pieces in order of bytes remaining.
	for _, index := range torrent.piecesByPendingBytesDesc() {
		if torrent.PieceNumPendingBytes(index) == torrent.PieceLength(index) {
			continue
		}
		for chunkSpec := range torrent.Pieces[index].PendingChunkSpecs {
			if !addRequest(Request{index, chunkSpec}) {
				return
			}
		}
	}
	if len(conn.Requests) == 0 {
		conn.SetInterested(false)
	}
}

func (me *Client) downloadedChunk(torrent *Torrent, msg *peer_protocol.Message) (err error) {
	request := Request{msg.Index, ChunkSpec{msg.Begin, peer_protocol.Integer(len(msg.Piece))}}
	if _, ok := torrent.Pieces[request.Index].PendingChunkSpecs[request.ChunkSpec]; !ok {
		log.Printf("got unnecessary chunk: %s", request)
		return
	}
	err = torrent.WriteChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	if err != nil {
		return
	}
	delete(torrent.Pieces[request.Index].PendingChunkSpecs, request.ChunkSpec)
	if len(torrent.Pieces[request.Index].PendingChunkSpecs) == 0 {
		me.queuePieceCheck(torrent, request.Index)
	}
	var next *list.Element
	for e := torrent.Priorities.Front(); e != nil; e = next {
		next = e.Next()
		if e.Value.(Request) == request {
			torrent.Priorities.Remove(e)
		}
	}
	me.dataReady(DataSpec{torrent.InfoHash, request})
	return
}

func (cl *Client) dataReady(ds DataSpec) {
	if cl.dataWaiter != nil {
		close(cl.dataWaiter)
	}
	cl.dataWaiter = nil
}

func (cl *Client) DataWaiter() <-chan struct{} {
	cl.Lock()
	defer cl.Unlock()
	if cl.dataWaiter == nil {
		cl.dataWaiter = make(chan struct{})
	}
	return cl.dataWaiter
}

func (me *Client) pieceHashed(t *Torrent, piece peer_protocol.Integer, correct bool) {
	p := t.Pieces[piece]
	p.EverHashed = true
	if correct {
		p.PendingChunkSpecs = nil
		log.Printf("got piece %d, (%d/%d)", piece, t.NumPiecesCompleted(), t.NumPieces())
		var next *list.Element
		if t.Priorities != nil {
			for e := t.Priorities.Front(); e != nil; e = next {
				next = e.Next()
				if e.Value.(Request).Index == piece {
					t.Priorities.Remove(e)
				}
			}
		}
		me.dataReady(DataSpec{
			t.InfoHash,
			Request{
				peer_protocol.Integer(piece),
				ChunkSpec{0, peer_protocol.Integer(t.PieceLength(piece))},
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

func (cl *Client) verifyPiece(t *Torrent, index peer_protocol.Integer) {
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

func (me *Client) Torrents() (ret []*Torrent) {
	me.mu.Lock()
	for _, t := range me.torrents {
		ret = append(ret, t)
	}
	me.mu.Unlock()
	return
}
