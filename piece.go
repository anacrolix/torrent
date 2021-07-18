package torrent

import (
	"fmt"
	"sync"

	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

type Piece struct {
	// The completed piece SHA1 hash, from the metainfo "pieces" field.
	hash  *metainfo.Hash
	t     *Torrent
	index pieceIndex
	files []*File
	// Chunks we've written to since the last check. The chunk offset and
	// length can be determined by the request chunkSize in use.
	_dirtyChunks bitmap.Bitmap

	numVerifies         int64
	hashing             bool
	marking             bool
	storageCompletionOk bool

	publicPieceState PieceState
	priority         piecePriority
	availability     int64

	// This can be locked when the Client lock is taken, but probably not vice versa.
	pendingWritesMutex sync.Mutex
	pendingWrites      int
	noPendingWrites    sync.Cond

	// Connections that have written data to this piece since its last check.
	// This can include connections that have closed.
	dirtiers map[*Peer]struct{}
}

func (p *Piece) String() string {
	return fmt.Sprintf("%s/%d", p.t.infoHash.HexString(), p.index)
}

func (p *Piece) Info() metainfo.Piece {
	return p.t.info.Piece(int(p.index))
}

func (p *Piece) Storage() storage.Piece {
	return p.t.storage.Piece(p.Info())
}

func (p *Piece) pendingChunkIndex(chunkIndex int) bool {
	return !p._dirtyChunks.Contains(bitmap.BitIndex(chunkIndex))
}

func (p *Piece) pendingChunk(cs ChunkSpec, chunkSize pp.Integer) bool {
	return p.pendingChunkIndex(chunkIndex(cs, chunkSize))
}

func (p *Piece) hasDirtyChunks() bool {
	return p._dirtyChunks.Len() != 0
}

func (p *Piece) numDirtyChunks() pp.Integer {
	return pp.Integer(p._dirtyChunks.Len())
}

func (p *Piece) unpendChunkIndex(i int) {
	p._dirtyChunks.Add(bitmap.BitIndex(i))
	p.t.tickleReaders()
}

func (p *Piece) pendChunkIndex(i int) {
	p._dirtyChunks.Remove(bitmap.BitIndex(i))
}

func (p *Piece) numChunks() pp.Integer {
	return p.t.pieceNumChunks(p.index)
}

func (p *Piece) incrementPendingWrites() {
	p.pendingWritesMutex.Lock()
	p.pendingWrites++
	p.pendingWritesMutex.Unlock()
}

func (p *Piece) decrementPendingWrites() {
	p.pendingWritesMutex.Lock()
	if p.pendingWrites == 0 {
		panic("assertion")
	}
	p.pendingWrites--
	if p.pendingWrites == 0 {
		p.noPendingWrites.Broadcast()
	}
	p.pendingWritesMutex.Unlock()
}

func (p *Piece) waitNoPendingWrites() {
	p.pendingWritesMutex.Lock()
	for p.pendingWrites != 0 {
		p.noPendingWrites.Wait()
	}
	p.pendingWritesMutex.Unlock()
}

func (p *Piece) isNoPendingWrites() bool {
	p.pendingWritesMutex.Lock()
	defer p.pendingWritesMutex.Unlock()
	return p.pendingWrites == 0
}

func (p *Piece) chunkIndexDirty(chunk pp.Integer) bool {
	return p._dirtyChunks.Contains(bitmap.BitIndex(chunk))
}

func (p *Piece) chunkIndexSpec(chunk pp.Integer) ChunkSpec {
	return chunkIndexSpec(chunk, p.length(), p.chunkSize())
}

func (p *Piece) chunkIndexRequest(chunkIndex pp.Integer) Request {
	return Request{
		pp.Integer(p.index),
		p.chunkIndexSpec(chunkIndex),
	}
}

func (p *Piece) numDirtyBytes() (ret pp.Integer) {
	// defer func() {
	// 	if ret > p.length() {
	// 		panic("too many dirty bytes")
	// 	}
	// }()
	numRegularDirtyChunks := p.numDirtyChunks()
	if p.chunkIndexDirty(p.numChunks() - 1) {
		numRegularDirtyChunks--
		ret += p.chunkIndexSpec(p.lastChunkIndex()).Length
	}
	ret += pp.Integer(numRegularDirtyChunks) * p.chunkSize()
	return
}

func (p *Piece) length() pp.Integer {
	return p.t.pieceLength(p.index)
}

func (p *Piece) chunkSize() pp.Integer {
	return p.t.chunkSize
}

func (p *Piece) lastChunkIndex() pp.Integer {
	return p.numChunks() - 1
}

func (p *Piece) bytesLeft() (ret pp.Integer) {
	if p.t.pieceComplete(p.index) {
		return 0
	}
	return p.length() - p.numDirtyBytes()
}

// Forces the piece data to be rehashed.
func (p *Piece) VerifyData() {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	target := p.numVerifies + 1
	if p.hashing {
		target++
	}
	//log.Printf("target: %d", target)
	p.t.queuePieceCheck(p.index)
	for {
		//log.Printf("got %d verifies", p.numVerifies)
		if p.numVerifies >= target {
			break
		}
		p.t.cl.event.Wait()
	}
	// log.Print("done")
}

func (p *Piece) queuedForHash() bool {
	return p.t.piecesQueuedForHash.Get(bitmap.BitIndex(p.index))
}

func (p *Piece) torrentBeginOffset() int64 {
	return int64(p.index) * p.t.info.PieceLength
}

func (p *Piece) torrentEndOffset() int64 {
	return p.torrentBeginOffset() + int64(p.length())
}

func (p *Piece) SetPriority(prio piecePriority) {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	p.priority = prio
	p.t.updatePiecePriority(p.index)
}

func (p *Piece) purePriority() (ret piecePriority) {
	for _, f := range p.files {
		ret.Raise(f.prio)
	}
	if p.t.readerNowPieces().Contains(bitmap.BitIndex(p.index)) {
		ret.Raise(PiecePriorityNow)
	}
	// if t._readerNowPieces.Contains(piece - 1) {
	// 	return PiecePriorityNext
	// }
	if p.t.readerReadaheadPieces().Contains(bitmap.BitIndex(p.index)) {
		ret.Raise(PiecePriorityReadahead)
	}
	ret.Raise(p.priority)
	return
}

func (p *Piece) uncachedPriority() (ret piecePriority) {
	if p.t.pieceComplete(p.index) || p.t.pieceQueuedForHash(p.index) || p.t.hashingPiece(p.index) {
		return PiecePriorityNone
	}
	return p.purePriority()
}

// Tells the Client to refetch the completion status from storage, updating priority etc. if
// necessary. Might be useful if you know the state of the piece data has changed externally.
func (p *Piece) UpdateCompletion() {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	p.t.updatePieceCompletion(p.index)
}

func (p *Piece) completion() (ret storage.Completion) {
	ret.Complete = p.t.pieceComplete(p.index)
	ret.Ok = p.storageCompletionOk
	return
}

func (p *Piece) allChunksDirty() bool {
	return p._dirtyChunks.Len() == bitmap.BitRange(p.numChunks())
}

func (p *Piece) dirtyChunks() bitmap.Bitmap {
	return p._dirtyChunks
}

func (p *Piece) State() PieceState {
	return p.t.PieceState(p.index)
}

func (p *Piece) iterUndirtiedChunks(f func(cs ChunkSpec) bool) bool {
	for i := pp.Integer(0); i < p.numChunks(); i++ {
		if p.chunkIndexDirty(i) {
			continue
		}
		if !f(p.chunkIndexSpec(i)) {
			return false
		}
	}
	return true
}
