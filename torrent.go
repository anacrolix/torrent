package torrent

import (
	"container/list"
	"fmt"
	"net"
	"sort"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	metainfo "github.com/nsf/libtorgo/torrent"
)

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

// Return the request that would include the given offset into the torrent data.
func torrentOffsetRequest(torrentLength, pieceSize, chunkSize, offset int64) (
	r Request, ok bool) {
	if offset < 0 || offset >= torrentLength {
		return
	}
	r.Index = peer_protocol.Integer(offset / pieceSize)
	r.Begin = peer_protocol.Integer(offset % pieceSize / chunkSize * chunkSize)
	left := torrentLength - int64(r.Index)*pieceSize - int64(r.Begin)
	if chunkSize < left {
		r.Length = peer_protocol.Integer(chunkSize)
	} else {
		r.Length = peer_protocol.Integer(left)
	}
	ok = true
	return
}

// Return the request that would include the given offset into the torrent data.
func (t *Torrent) offsetRequest(off int64) (req Request, ok bool) {
	return torrentOffsetRequest(t.Length(), t.MetaInfo.PieceLength, chunkSize, off)
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

func (t *Torrent) wantPiece(index int) bool {
	p := t.Pieces[index]
	return p.EverHashed && len(p.PendingChunkSpecs) != 0
}
