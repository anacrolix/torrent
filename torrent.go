package torrent

import (
	"container/heap"
	"container/list"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"sync"

	"bitbucket.org/anacrolix/go.torrent/util"

	"bitbucket.org/anacrolix/go.torrent/mmap_span"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"github.com/anacrolix/libtorgo/bencode"
	"github.com/anacrolix/libtorgo/metainfo"
)

func (t *torrent) PieceNumPendingBytes(index pp.Integer) (count pp.Integer) {
	pendingChunks := t.Pieces[index].PendingChunkSpecs
	count = pp.Integer(len(pendingChunks)) * chunkSize
	_lastChunkSpec := lastChunkSpec(t.PieceLength(index))
	if _lastChunkSpec.Length != chunkSize {
		if _, ok := pendingChunks[_lastChunkSpec]; ok {
			count += _lastChunkSpec.Length - chunkSize
		}
	}
	return
}

type pieceBytesLeft struct {
	Piece, BytesLeft int
}

type torrentPiece struct {
	piece
	bytesLeftElement *list.Element
}

type peersKey struct {
	IPBytes string
	Port    int
}

type torrent struct {
	stateMu sync.Mutex
	closing chan struct{}

	// Closed when no more network activity is desired. This includes
	// announcing, and communicating with peers.
	ceasingNetworking chan struct{}

	InfoHash                    InfoHash
	Pieces                      []*torrentPiece
	IncompletePiecesByBytesLeft *OrderedList
	length                      int64
	// Prevent mutations to Data memory maps while in use as they're not safe.
	dataLock sync.RWMutex
	Data     mmap_span.MMapSpan

	Info *MetaInfo
	// Active peer connections.
	Conns []*connection
	// Set of addrs to which we're attempting to connect.
	HalfOpen map[string]struct{}

	// Reserve of peers to connect to. A peer can be both here and in the
	// active connections if were told about the peer after connecting with
	// them. That encourages us to reconnect to peers that are well known.
	Peers     map[peersKey]Peer
	wantPeers sync.Cond

	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list key.
	Trackers     [][]tracker.Client
	DisplayName  string
	MetaData     []byte
	metadataHave []bool

	gotMetainfo chan struct{}
	GotMetainfo <-chan struct{}
}

func (t *torrent) addrActive(addr string) bool {
	if _, ok := t.HalfOpen[addr]; ok {
		return true
	}
	for _, c := range t.Conns {
		if c.Socket.RemoteAddr().String() == addr {
			return true
		}
	}
	return false
}

func (t *torrent) worstConnsHeap() (wcs *worstConns) {
	wcs = &worstConns{
		c: append([]*connection{}, t.Conns...),
		t: t,
	}
	heap.Init(wcs)
	return
}

func (t *torrent) CeaseNetworking() {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	select {
	case <-t.ceasingNetworking:
		return
	default:
	}
	close(t.ceasingNetworking)
	for _, c := range t.Conns {
		c.Close()
	}
}

func (t *torrent) assertIncompletePiecesByBytesLeftOrdering() {
	allIndexes := make(map[int]struct{}, t.NumPieces())
	for i := 0; i < t.NumPieces(); i++ {
		allIndexes[i] = struct{}{}
	}
	var lastBytesLeft int
	for e := t.IncompletePiecesByBytesLeft.Front(); e != nil; e = e.Next() {
		i := e.Value.(int)
		if _, ok := allIndexes[i]; !ok {
			panic("duplicate entry")
		}
		delete(allIndexes, i)
		if t.Pieces[i].Complete() {
			panic("complete piece")
		}
		bytesLeft := int(t.PieceNumPendingBytes(pp.Integer(i)))
		if bytesLeft < lastBytesLeft {
			panic("ordering broken")
		}
		lastBytesLeft = bytesLeft
	}
	for i := range allIndexes {
		if !t.Pieces[i].Complete() {
			panic("leaked incomplete piece")
		}
	}
}

func (t *torrent) AddPeers(pp []Peer) {
	for _, p := range pp {
		t.Peers[peersKey{string(p.IP), p.Port}] = p
	}
}

func (t *torrent) InvalidateMetadata() {
	t.MetaData = nil
	t.metadataHave = nil
	t.Info = nil
}

func (t *torrent) SaveMetadataPiece(index int, data []byte) {
	if t.haveInfo() {
		return
	}
	if index >= len(t.metadataHave) {
		log.Printf("%s: ignoring metadata piece %d", t, index)
		return
	}
	copy(t.MetaData[(1<<14)*index:], data)
	t.metadataHave[index] = true
}

func (t *torrent) MetadataPieceCount() int {
	return (len(t.MetaData) + (1 << 14) - 1) / (1 << 14)
}

func (t *torrent) HaveMetadataPiece(piece int) bool {
	if t.haveInfo() {
		return (1<<14)*piece < len(t.MetaData)
	} else {
		return piece < len(t.metadataHave) && t.metadataHave[piece]
	}
}

func (t *torrent) metadataSizeKnown() bool {
	return t.MetaData != nil
}

func (t *torrent) metadataSize() int {
	return len(t.MetaData)
}

func infoPieceHashes(info *metainfo.Info) (ret []string) {
	for i := 0; i < len(info.Pieces); i += 20 {
		ret = append(ret, string(info.Pieces[i:i+20]))
	}
	return
}

// Called when metadata for a torrent becomes available.
func (t *torrent) setMetadata(md metainfo.Info, dataDir string, infoBytes []byte) (err error) {
	t.Info = newMetaInfo(&md)
	t.MetaData = infoBytes
	t.metadataHave = nil
	t.Data, err = mmapTorrentData(&md, dataDir)
	if err != nil {
		return
	}
	t.length = t.Data.Size()
	t.IncompletePiecesByBytesLeft = NewList(func(a, b interface{}) bool {
		apb := t.PieceNumPendingBytes(pp.Integer(a.(int)))
		bpb := t.PieceNumPendingBytes(pp.Integer(b.(int)))
		if apb < bpb {
			return true
		}
		if apb > bpb {
			return false
		}
		return a.(int) < b.(int)
	})
	for index, hash := range infoPieceHashes(&md) {
		piece := &torrentPiece{}
		util.CopyExact(piece.Hash[:], hash)
		t.Pieces = append(t.Pieces, piece)
		piece.bytesLeftElement = t.IncompletePiecesByBytesLeft.Insert(index)
		t.pendAllChunkSpecs(pp.Integer(index))
	}
	t.assertIncompletePiecesByBytesLeftOrdering()
	for _, conn := range t.Conns {
		if err := conn.setNumPieces(t.NumPieces()); err != nil {
			log.Printf("closing connection: %s", err)
			conn.Close()
		}
	}
	return
}

func (t *torrent) HaveAllMetadataPieces() bool {
	if t.haveInfo() {
		return true
	}
	if t.metadataHave == nil {
		return false
	}
	for _, have := range t.metadataHave {
		if !have {
			return false
		}
	}
	return true
}

func (t *torrent) SetMetadataSize(bytes int64) {
	if t.MetaData != nil {
		return
	}
	t.MetaData = make([]byte, bytes)
	t.metadataHave = make([]bool, (bytes+(1<<14)-1)/(1<<14))
}

func (t *torrent) Name() string {
	if !t.haveInfo() {
		return t.DisplayName
	}
	return t.Info.Name
}

func (t *torrent) pieceStatusChar(index int) byte {
	p := t.Pieces[index]
	switch {
	case p.Complete():
		return 'C'
	case p.QueuedForHash:
		return 'Q'
	case p.Hashing:
		return 'H'
	case !p.EverHashed:
		return '?'
	case t.PiecePartiallyDownloaded(index):
		return 'P'
	default:
		return '.'
	}
}

func (t *torrent) metadataPieceSize(piece int) int {
	return metadataPieceSize(len(t.MetaData), piece)
}

func (t *torrent) NewMetadataExtensionMessage(c *connection, msgType int, piece int, data []byte) pp.Message {
	d := map[string]int{
		"msg_type": msgType,
		"piece":    piece,
	}
	if data != nil {
		d["total_size"] = len(t.MetaData)
	}
	p, err := bencode.Marshal(d)
	if err != nil {
		panic(err)
	}
	return pp.Message{
		Type:            pp.Extended,
		ExtendedID:      byte(c.PeerExtensionIDs["ut_metadata"]),
		ExtendedPayload: append(p, data...),
	}
}

func (t *torrent) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "Infohash: %x\n", t.InfoHash)
	fmt.Fprintf(w, "Piece length: %s\n", func() string {
		if t.haveInfo() {
			return fmt.Sprint(t.UsualPieceSize())
		} else {
			return "?"
		}
	}())
	fmt.Fprint(w, "Pieces:")
	for index := range t.Pieces {
		if index%100 == 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%c", t.pieceStatusChar(index))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Trackers: ")
	for _, tier := range t.Trackers {
		for _, tr := range tier {
			fmt.Fprintf(w, "%q ", tr.String())
		}
	}
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "Pending peers: %d\n", len(t.Peers))
	fmt.Fprintf(w, "Half open: %d\n", len(t.HalfOpen))
	fmt.Fprintf(w, "Active peers: %d\n", len(t.Conns))
	sort.Sort(&worstConns{
		c: t.Conns,
		t: t,
	})
	for _, c := range t.Conns {
		c.WriteStatus(w)
	}
}

func (t *torrent) String() string {
	s := t.Name()
	if s == "" {
		s = fmt.Sprintf("%x", t.InfoHash)
	}
	return s
}

func (t *torrent) haveInfo() bool {
	return t.Info != nil
}

// TODO: Include URIs that weren't converted to tracker clients.
func (t *torrent) AnnounceList() (al [][]string) {
	for _, tier := range t.Trackers {
		var l []string
		for _, tr := range tier {
			l = append(l, tr.URL())
		}
		al = append(al, l)
	}
	return
}

func (t *torrent) MetaInfo() *metainfo.MetaInfo {
	return &metainfo.MetaInfo{
		Info: metainfo.InfoEx{
			Info: *t.Info.Info,
		},
		CreationDate: time.Now().Unix(),
		Comment:      "dynamic metainfo from client",
		CreatedBy:    "go.torrent",
		AnnounceList: t.AnnounceList(),
	}
}

func (t *torrent) BytesLeft() (left int64) {
	if !t.haveInfo() {
		return -1
	}
	for i := pp.Integer(0); i < pp.Integer(t.NumPieces()); i++ {
		left += int64(t.PieceNumPendingBytes(i))
	}
	return
}

func (t *torrent) PiecePartiallyDownloaded(index int) bool {
	return t.PieceNumPendingBytes(pp.Integer(index)) != t.PieceLength(pp.Integer(index))
}

func NumChunksForPiece(chunkSize int, pieceSize int) int {
	return (pieceSize + chunkSize - 1) / chunkSize
}

func (t *torrent) ChunkCount() (num int) {
	num += (t.NumPieces() - 1) * NumChunksForPiece(chunkSize, int(t.PieceLength(0)))
	num += NumChunksForPiece(chunkSize, int(t.PieceLength(pp.Integer(t.NumPieces()-1))))
	return
}

func (t *torrent) UsualPieceSize() int {
	return int(t.Info.PieceLength)
}

func (t *torrent) LastPieceSize() int {
	return int(t.PieceLength(pp.Integer(t.NumPieces() - 1)))
}

func (t *torrent) NumPieces() int {
	return len(t.Info.Pieces) / 20
}

func (t *torrent) NumPiecesCompleted() (num int) {
	for _, p := range t.Pieces {
		if p.Complete() {
			num++
		}
	}
	return
}

func (t *torrent) Length() int64 {
	if t.Data == nil {
		// Possibly the length might be available before the data is mmapped,
		// I defer this decision to such a need arising.
		panic("torrent length not known?")
	}
	return t.length
}

func (t *torrent) isClosed() bool {
	select {
	case <-t.closing:
		return true
	default:
		return false
	}
}

func (t *torrent) Close() (err error) {
	if t.isClosed() {
		return
	}
	t.CeaseNetworking()
	close(t.closing)
	t.dataLock.Lock()
	t.Data.Close()
	t.Data = nil
	t.dataLock.Unlock()
	for _, conn := range t.Conns {
		conn.Close()
	}
	return
}

// Return the request that would include the given offset into the torrent data.
func torrentOffsetRequest(torrentLength, pieceSize, chunkSize, offset int64) (
	r request, ok bool) {
	if offset < 0 || offset >= torrentLength {
		return
	}
	r.Index = pp.Integer(offset / pieceSize)
	r.Begin = pp.Integer(offset % pieceSize / chunkSize * chunkSize)
	left := torrentLength - int64(r.Index)*pieceSize - int64(r.Begin)
	if chunkSize < left {
		r.Length = pp.Integer(chunkSize)
	} else {
		r.Length = pp.Integer(left)
	}
	ok = true
	return
}

func torrentRequestOffset(torrentLength, pieceSize int64, r request) (off int64) {
	off = int64(r.Index)*pieceSize + int64(r.Begin)
	if off < 0 || off >= torrentLength {
		panic("invalid request")
	}
	return
}

func (t *torrent) requestOffset(r request) int64 {
	return torrentRequestOffset(t.Length(), int64(t.UsualPieceSize()), r)
}

// Return the request that would include the given offset into the torrent data.
func (t *torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.Length(), t.Info.PieceLength, chunkSize, off)
}

func (t *torrent) WriteChunk(piece int, begin int64, data []byte) (err error) {
	_, err = t.Data.WriteAt(data, int64(piece)*t.Info.PieceLength+begin)
	return
}

func (t *torrent) bitfield() (bf []bool) {
	for _, p := range t.Pieces {
		bf = append(bf, p.EverHashed && len(p.PendingChunkSpecs) == 0)
	}
	return
}

func (t *torrent) pieceChunks(piece int) (css []chunkSpec) {
	css = make([]chunkSpec, 0, (t.PieceLength(pp.Integer(piece))+chunkSize-1)/chunkSize)
	var cs chunkSpec
	for left := t.PieceLength(pp.Integer(piece)); left != 0; left -= cs.Length {
		cs.Length = left
		if cs.Length > chunkSize {
			cs.Length = chunkSize
		}
		css = append(css, cs)
		cs.Begin += cs.Length
	}
	return
}

func (t *torrent) pendAllChunkSpecs(index pp.Integer) {
	piece := t.Pieces[index]
	if piece.PendingChunkSpecs == nil {
		piece.PendingChunkSpecs = make(
			map[chunkSpec]struct{},
			(t.PieceLength(index)+chunkSize-1)/chunkSize)
	}
	pcss := piece.PendingChunkSpecs
	for _, cs := range t.pieceChunks(int(index)) {
		pcss[cs] = struct{}{}
	}
	t.IncompletePiecesByBytesLeft.ValueChanged(piece.bytesLeftElement)
	return
}

func (t *torrent) PieceBytesLeftChanged(index int) {
	p := t.Pieces[index]
	if p.Complete() {
		t.IncompletePiecesByBytesLeft.Remove(p.bytesLeftElement)
	} else {
		t.IncompletePiecesByBytesLeft.ValueChanged(p.bytesLeftElement)
	}
}

type Peer struct {
	Id     [20]byte
	IP     net.IP
	Port   int
	Source peerSource
}

func (t *torrent) PieceLength(piece pp.Integer) (len_ pp.Integer) {
	if int(piece) == t.NumPieces()-1 {
		len_ = pp.Integer(t.Length() % t.Info.PieceLength)
	}
	if len_ == 0 {
		len_ = pp.Integer(t.Info.PieceLength)
	}
	return
}

func (t *torrent) HashPiece(piece pp.Integer) (ps pieceSum) {
	hash := pieceHash.New()
	t.dataLock.RLock()
	n, err := t.Data.WriteSectionTo(hash, int64(piece)*t.Info.PieceLength, t.Info.PieceLength)
	t.dataLock.RUnlock()
	if err != nil {
		panic(err)
	}
	if pp.Integer(n) != t.PieceLength(piece) {
		// log.Print(t.Info)
		panic(fmt.Sprintf("hashed wrong number of bytes: expected %d; did %d; piece %d", t.PieceLength(piece), n, piece))
	}
	util.CopyExact(ps[:], hash.Sum(nil))
	return
}
func (t *torrent) haveAllPieces() bool {
	if !t.haveInfo() {
		return false
	}
	for _, piece := range t.Pieces {
		if !piece.Complete() {
			return false
		}
	}
	return true
}

func (me *torrent) haveAnyPieces() bool {
	for _, piece := range me.Pieces {
		if piece.Complete() {
			return true
		}
	}
	return false
}

func (t *torrent) havePiece(index int) bool {
	return t.haveInfo() && t.Pieces[index].Complete()
}

func (t *torrent) haveChunk(r request) bool {
	p := t.Pieces[r.Index]
	if !p.EverHashed {
		return false
	}
	_, ok := p.PendingChunkSpecs[r.chunkSpec]
	return !ok
}

func (t *torrent) wantChunk(r request) bool {
	if !t.wantPiece(int(r.Index)) {
		return false
	}
	_, ok := t.Pieces[r.Index].PendingChunkSpecs[r.chunkSpec]
	return ok
}

func (t *torrent) wantPiece(index int) bool {
	if !t.haveInfo() {
		return false
	}
	p := t.Pieces[index]
	return p.EverHashed && len(p.PendingChunkSpecs) != 0
}

func (t *torrent) connHasWantedPieces(c *connection) bool {
	for p := range t.Pieces {
		if t.wantPiece(p) && c.PeerHasPiece(pp.Integer(p)) {
			return true
		}
	}
	return false
}

func (t *torrent) extentPieces(off, _len int64) (pieces []int) {
	for i := off / int64(t.UsualPieceSize()); i*int64(t.UsualPieceSize()) < off+_len; i++ {
		pieces = append(pieces, int(i))
	}
	return
}
