package torrent

import (
	"container/list"
	"fmt"
	"io"
	"log"
	"net"
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
	closed            bool
	InfoHash          InfoHash
	Pieces            []*torrentPiece
	PiecesByBytesLeft *OrderedList
	Data              mmap_span.MMapSpan
	// Prevent mutations to Data memory maps while in use as they're not safe.
	dataLock sync.RWMutex
	Info     *metainfo.Info
	Conns    []*connection
	Peers    map[peersKey]Peer
	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list key.
	Trackers     [][]tracker.Client
	DisplayName  string
	MetaData     []byte
	metadataHave []bool
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
		return t.metadataHave[piece]
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
	t.Info = &md
	t.MetaData = infoBytes
	t.metadataHave = nil
	t.Data, err = mmapTorrentData(&md, dataDir)
	if err != nil {
		return
	}
	t.PiecesByBytesLeft = NewList(func(a, b interface{}) bool {
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
		piece.bytesLeftElement = t.PiecesByBytesLeft.Insert(index)
		t.pendAllChunkSpecs(pp.Integer(index))
	}
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
	fmt.Fprint(w, "Pieces: ")
	for index := range t.Pieces {
		fmt.Fprintf(w, "%c", t.pieceStatusChar(index))
	}
	fmt.Fprintln(w)
	// fmt.Fprintln(w, "Priorities: ")
	// if t.Priorities != nil {
	// 	for e := t.Priorities.Front(); e != nil; e = e.Next() {
	// 		fmt.Fprintf(w, "\t%v\n", e.Value)
	// 	}
	// }
	fmt.Fprintf(w, "Pending peers: %d\n", len(t.Peers))
	for _, c := range t.Conns {
		c.WriteStatus(w)
	}
}

func (t *torrent) String() string {
	return t.Name()
}

func (t *torrent) haveInfo() bool {
	return t.Info != nil
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
	return int64(t.LastPieceSize()) + int64(len(t.Pieces)-1)*int64(t.UsualPieceSize())
}

func (t *torrent) isClosed() bool {
	return t.closed
}

func (t *torrent) Close() (err error) {
	t.closed = true
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
	t.PiecesByBytesLeft.ValueChanged(piece.bytesLeftElement)
	return
}

type Peer struct {
	Id     [20]byte
	IP     net.IP
	Port   int
	Source peerSource
}

func (t *torrent) PieceLength(piece pp.Integer) (len_ pp.Integer) {
	if int(piece) == t.NumPieces()-1 {
		len_ = pp.Integer(t.Data.Size() % t.Info.PieceLength)
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
