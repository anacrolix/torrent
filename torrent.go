package torrent

import (
	"container/list"
	"fmt"
	"io"
	"log"
	"net"
	"sort"

	"bitbucket.org/anacrolix/go.torrent/mmap_span"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
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

type torrent struct {
	InfoHash   InfoHash
	Pieces     []*piece
	Data       mmap_span.MMapSpan
	Info       MetaData
	Conns      []*connection
	Peers      []Peer
	Priorities *list.List
	// BEP 12 Multitracker Metadata Extension. The tracker.Client instances
	// mirror their respective URLs from the announce-list key.
	Trackers      [][]tracker.Client
	lastReadPiece int
	DisplayName   string
	MetaData      []byte
	MetaDataHave  []bool
}

func (t *torrent) GotAllMetadataPieces() bool {
	if t.MetaDataHave == nil {
		return false
	}
	for _, have := range t.MetaDataHave {
		if !have {
			return false
		}
	}
	return true
}

func (t *torrent) SetMetaDataSize(bytes int64) {
	if t.MetaData != nil {
		if len(t.MetaData) != int(bytes) {
			log.Printf("new metadata_size differs")
		}
		return
	}
	t.MetaData = make([]byte, bytes)
	t.MetaDataHave = make([]bool, (bytes+(1<<14)-1)/(1<<14))
}

func (t *torrent) Name() string {
	if t.Info == nil {
		return t.DisplayName
	}
	return t.Info.Name()
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

func (t *torrent) WriteStatus(w io.Writer) {
	fmt.Fprint(w, "Pieces: ")
	for index := range t.Pieces {
		fmt.Fprintf(w, "%c", t.pieceStatusChar(index))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Priorities: ")
	for e := t.Priorities.Front(); e != nil; e = e.Next() {
		fmt.Fprintf(w, "\t%v\n", e.Value)
	}
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
	return int(t.Info.PieceLength())
}

func (t *torrent) LastPieceSize() int {
	return int(t.PieceLength(pp.Integer(t.NumPieces() - 1)))
}

func (t *torrent) NumPieces() int {
	return t.Info.PieceCount()
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
	return int64(t.PieceLength(pp.Integer(len(t.Pieces)-1))) + int64(len(t.Pieces)-1)*int64(t.PieceLength(0))
}

func (t *torrent) Close() (err error) {
	t.Data.Close()
	for _, conn := range t.Conns {
		conn.Close()
	}
	return
}

func (t *torrent) piecesByPendingBytes() (indices []pp.Integer) {
	slice := pieceByBytesPendingSlice{
		Pending: make([]pp.Integer, 0, len(t.Pieces)),
		Indices: make([]pp.Integer, 0, len(t.Pieces)),
	}
	for i := range t.Pieces {
		slice.Pending = append(slice.Pending, t.PieceNumPendingBytes(pp.Integer(i)))
		slice.Indices = append(slice.Indices, pp.Integer(i))
	}
	sort.Sort(slice)
	return slice.Indices
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
	return torrentRequestOffset(t.Length(), t.Info.PieceLength(), r)
}

// Return the request that would include the given offset into the torrent data.
func (t *torrent) offsetRequest(off int64) (req request, ok bool) {
	return torrentOffsetRequest(t.Length(), t.Info.PieceLength(), chunkSize, off)
}

func (t *torrent) WriteChunk(piece int, begin int64, data []byte) (err error) {
	_, err = t.Data.WriteAt(data, int64(piece)*t.Info.PieceLength()+begin)
	return
}

func (t *torrent) bitfield() (bf []bool) {
	for _, p := range t.Pieces {
		bf = append(bf, p.EverHashed && len(p.PendingChunkSpecs) == 0)
	}
	return
}

func (t *torrent) pendAllChunkSpecs(index pp.Integer) {
	piece := t.Pieces[index]
	if piece.PendingChunkSpecs == nil {
		piece.PendingChunkSpecs = make(
			map[chunkSpec]struct{},
			(t.Info.PieceLength()+chunkSize-1)/chunkSize)
	}
	c := chunkSpec{
		Begin: 0,
	}
	cs := piece.PendingChunkSpecs
	for left := pp.Integer(t.PieceLength(index)); left != 0; left -= c.Length {
		c.Length = left
		if c.Length > chunkSize {
			c.Length = chunkSize
		}
		cs[c] = struct{}{}
		c.Begin += c.Length
	}
	return
}

type Peer struct {
	Id   [20]byte
	IP   net.IP
	Port int
}

func (t *torrent) PieceLength(piece pp.Integer) (len_ pp.Integer) {
	if int(piece) == t.NumPieces()-1 {
		len_ = pp.Integer(t.Data.Size() % t.Info.PieceLength())
	}
	if len_ == 0 {
		len_ = pp.Integer(t.Info.PieceLength())
	}
	return
}

func (t *torrent) HashPiece(piece pp.Integer) (ps pieceSum) {
	hash := pieceHash.New()
	n, err := t.Data.WriteSectionTo(hash, int64(piece)*t.Info.PieceLength(), t.Info.PieceLength())
	if err != nil {
		panic(err)
	}
	if pp.Integer(n) != t.PieceLength(piece) {
		panic(fmt.Sprintf("hashed wrong number of bytes: expected %d; did %d; piece %d", t.PieceLength(piece), n, piece))
	}
	copyHashSum(ps[:], hash.Sum(nil))
	return
}
func (t *torrent) haveAllPieces() bool {
	if t.Info == nil {
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

func (t *torrent) wantPiece(index int) bool {
	if !t.haveInfo() {
		return false
	}
	p := t.Pieces[index]
	return p.EverHashed && len(p.PendingChunkSpecs) != 0
}
