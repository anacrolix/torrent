package torrent

import (
	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bufio"
	"container/list"
	"crypto"
	"crypto/rand"
	"encoding"
	"errors"
	"fmt"
	metainfo "github.com/nsf/libtorgo/torrent"
	"io"
	"launchpad.net/gommap"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
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
	EverHashed        bool
}

func (p *piece) Complete() bool {
	return len(p.PendingChunkSpecs) == 0 && !p.Hashing && p.EverHashed
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
	post   chan encoding.BinaryMarshaler
	write  chan []byte

	Interested bool
	Choked     bool
	Requests   map[Request]struct{}

	PeerId         [20]byte
	PeerInterested bool
	PeerChoked     bool
	PeerRequests   map[Request]struct{}
	PeerExtensions [8]byte
	PeerPieces     []bool
}

func (c *Connection) Close() {
	c.Socket.Close()
	close(c.post)
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

func (c *Connection) Request(chunk Request) bool {
	if len(c.Requests) >= maxRequests {
		return false
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
		log.Printf("wrote %#v", string(b))
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

func (cl *Client) queuePieceCheck(t *Torrent, pieceIndex peer_protocol.Integer) {
	piece := t.Pieces[pieceIndex]
	if piece.Hashing {
		return
	}
	piece.Hashing = true
	go cl.verifyPiece(t, pieceIndex)
}

func (cl *Client) PrioritizeDataRegion(ih InfoHash, off, len_ int64) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	t := cl.torrent(ih)
	newPriorities := make([]Request, 0, (len_+2*(chunkSize-1))/chunkSize)
	for len_ > 0 {
		index := peer_protocol.Integer(off / int64(t.PieceLength(0)))
		pieceOff := peer_protocol.Integer(off % int64(t.PieceLength(0)))
		piece := t.Pieces[index]
		if !piece.EverHashed {
			cl.queuePieceCheck(t, index)
		}
		chunk := ChunkSpec{pieceOff / chunkSize * chunkSize, chunkSize}
		adv := int64(chunkSize - pieceOff%chunkSize)
		off += adv
		len_ -= adv
		if _, ok := piece.PendingChunkSpecs[chunk]; !ok && !piece.Hashing {
			continue
		}
		newPriorities = append(newPriorities, Request{index, chunk})
	}
	if len(newPriorities) < 1 {
		return
	}
	log.Print(newPriorities)
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
		bf = append(bf, p.EverHashed && !p.Hashing && len(p.PendingChunkSpecs) == 0)
	}
	return
}

func (t *Torrent) pieceChunkSpecs(index peer_protocol.Integer) (cs map[ChunkSpec]struct{}) {
	cs = make(map[ChunkSpec]struct{}, (t.MetaInfo.PieceLength+chunkSize-1)/chunkSize)
	c := ChunkSpec{
		Begin: 0,
	}
	for left := peer_protocol.Integer(t.PieceLength(index)); left > 0; left -= c.Length {
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
	if int(piece) == len(t.Pieces)-1 {
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
	DataDir       string
	HalfOpenLimit int
	PeerId        [20]byte
	DataReady     chan DataSpec

	sync.Mutex
	mu    *sync.Mutex
	event sync.Cond

	halfOpen int
	torrents map[InfoHash]*Torrent
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
	index := peer_protocol.Integer(off / int64(t.PieceLength(0)))
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
		err = me.runConnection(conn, torrent, peer.Id)
		if err != nil {
			log.Print(err)
		}
	}()
}

func (me *Torrent) haveAnyPieces() bool {
	for _, piece := range me.Pieces {
		if piece.Complete() {
			return true
		}
	}
	return false
}

func (me *Client) runConnection(sock net.Conn, torrent *Torrent, peerId [20]byte) (err error) {
	conn := &Connection{
		Socket:     sock,
		Choked:     true,
		PeerChoked: true,
		write:      make(chan []byte),
		post:       make(chan encoding.BinaryMarshaler),
	}
	defer conn.Close()
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
	log.Printf("peer extensions: %#v", string(conn.PeerExtensions[:]))
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
	me.addConnection(torrent, conn)
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
	me.mu.Unlock()
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
	return p.EverHashed && !p.Hashing && len(p.PendingChunkSpecs) != 0
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
		msg := new(peer_protocol.Message)
		err := decoder.Decode(msg)
		me.mu.Lock()
		if err != nil {
			return err
		}
		log.Print(msg.Type)
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
		case peer_protocol.NotInterested:
			conn.PeerInterested = false
		case peer_protocol.Have:
			me.peerGotPiece(torrent, conn, int(msg.Index))
		case peer_protocol.Request:
			conn.PeerRequests[Request{
				Index:     msg.Index,
				ChunkSpec: ChunkSpec{msg.Begin, msg.Length},
			}] = struct{}{}
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
	for _, c := range t.Conns {
		if c.PeerId == c.PeerId {
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

func (me *Client) AddTorrent(metaInfo *metainfo.MetaInfo) error {
	torrent := &Torrent{
		InfoHash: BytesInfoHash(metaInfo.InfoHash),
		MetaInfo: metaInfo,
	}
	for offset := 0; offset < len(metaInfo.Pieces); offset += PieceHash.Size() {
		hash := metaInfo.Pieces[offset : offset+PieceHash.Size()]
		if len(hash) != PieceHash.Size() {
			return errors.New("bad piece hash in metainfo")
		}
		piece := &piece{}
		copyHashSum(piece.Hash[:], hash)
		torrent.Pieces = append(torrent.Pieces, piece)
	}
	var err error
	torrent.Data, err = mmapTorrentData(metaInfo, me.DataDir)
	if err != nil {
		return err
	}
	me.mu.Lock()
	defer me.mu.Unlock()
	if _, ok := me.torrents[torrent.InfoHash]; ok {
		return torrent.Close()
	}
	me.torrents[torrent.InfoHash] = torrent
	return nil
}

func (me *Client) WaitAll() {
	me.mu.Lock()
	for len(me.torrents) != 0 {
		me.event.Wait()
	}
	me.mu.Unlock()
}

func (me *Client) Stop() {
}

func (me *Client) replenishConnRequests(torrent *Torrent, conn *Connection) {
	requestHeatMap := torrent.requestHeat()
	addRequest := func(req Request) (again bool) {
		piece := torrent.Pieces[req.Index]
		if piece.Hashing {
			return true
		}
		if piece.Complete() {
			return true
		}
		if requestHeatMap[req] > 0 {
			return true
		}
		return conn.Request(req)
	}
	if torrent.Priorities != nil {
		for e := torrent.Priorities.Front(); e != nil; e = e.Next() {
			if !addRequest(e.Value.(Request)) {
				return
			}
		}
	}
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
	conn.SetInterested(false)
}

func (me *Client) downloadedChunk(torrent *Torrent, msg *peer_protocol.Message) (err error) {
	request := Request{msg.Index, ChunkSpec{msg.Begin, peer_protocol.Integer(len(msg.Piece))}}
	if _, ok := torrent.Pieces[request.Index].PendingChunkSpecs[request.ChunkSpec]; !ok {
		log.Printf("got unnecessary chunk: %s", request)
		return
	}
	log.Printf("got chunk %s", request)
	err = torrent.WriteChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	if err != nil {
		return
	}
	delete(torrent.Pieces[request.Index].PendingChunkSpecs, request.ChunkSpec)
	if len(torrent.Pieces[request.Index].PendingChunkSpecs) == 0 {
		me.queuePieceCheck(torrent, request.Index)
		return
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
	if cl.DataReady == nil {
		return
	}
	go func() {
		cl.DataReady <- ds
	}()
}

func (me *Client) pieceHashed(t *Torrent, piece peer_protocol.Integer, correct bool) {
	p := t.Pieces[piece]
	if !p.Hashing {
		panic("invalid state")
	}
	p.Hashing = false
	p.EverHashed = true
	if correct {
		p.PendingChunkSpecs = nil
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
			p.PendingChunkSpecs = t.pieceChunkSpecs(piece)
		}
	}
	for _, conn := range t.Conns {
		if correct {
			conn.Post(peer_protocol.Message{
				Type:  peer_protocol.Have,
				Index: peer_protocol.Integer(piece),
			})
		} else {
			if conn.PeerHasPiece(piece) {
				me.replenishConnRequests(t, conn)
			}
		}
	}
}

func (cl *Client) verifyPiece(t *Torrent, index peer_protocol.Integer) {
	sum := t.HashPiece(index)
	cl.mu.Lock()
	piece := t.Pieces[index]
	cl.pieceHashed(t, index, sum == piece.Hash)
	piece.Hashing = false
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
