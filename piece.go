package torrent

import (
	"fmt"
	"sync"

	"github.com/anacrolix/chansync"
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
	mu    sync.RWMutex

	readerCond chansync.BroadcastCond

	numVerifies         int64
	hashing             bool
	marking             bool
	storageCompletionOk bool

	publicPieceState PieceState
	priority         piecePriority
	// Availability adjustment for this piece relative to len(Torrent.connsWithAllPieces). This is
	// incremented for any piece a peer has when a peer has a piece, Torrent.haveInfo is true, and
	// the Peer isn't recorded in Torrent.connsWithAllPieces.
	relativeAvailability int

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

func (p *Piece) Flush() {
	if p.t.storage.Flush != nil {
		_ = p.t.storage.Flush()
	}
}

func (p *Piece) pendingChunkIndex(chunkIndex chunkIndexType) bool {
	return !p.chunkIndexDirty(chunkIndex)
}

func (p *Piece) pendingChunk(cs ChunkSpec, chunkSize pp.Integer) bool {
	return p.pendingChunkIndex(chunkIndexFromChunkSpec(cs, chunkSize))
}

func (p *Piece) hasDirtyChunks(lock bool) bool {
	return p.numDirtyChunks(lock) != 0
}

func (p *Piece) numDirtyChunks(lock bool) chunkIndexType {
	if lock {
		p.t.mu.RLock()
		defer p.t.mu.RUnlock()
	}
	return chunkIndexType(roaringBitmapRangeCardinality[RequestIndex](
		&p.t.dirtyChunks,
		p.requestIndexOffset(),
		p.t.pieceRequestIndexOffset(p.index+1)))
}

func (p *Piece) unpendChunkIndex(i chunkIndexType) {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	p.t.dirtyChunks.Add(p.requestIndexOffset() + i)
	p.t.updatePieceRequestOrderPiece(p.index, false)
	p.readerCond.Broadcast()
}

func (p *Piece) pendChunkIndex(i RequestIndex) {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	p.t.dirtyChunks.Remove(p.requestIndexOffset() + i)
	p.t.updatePieceRequestOrderPiece(p.index, false)
}

func (p *Piece) numChunks() chunkIndexType {
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

func (p *Piece) chunkIndexDirty(chunk chunkIndexType) bool {
	p.t.mu.RLock()
	defer p.t.mu.RUnlock()
	return p.t.dirtyChunks.Contains(p.requestIndexOffset() + chunk)
}

func (p *Piece) chunkIndexSpec(chunk chunkIndexType) ChunkSpec {
	return chunkIndexSpec(pp.Integer(chunk), p.length(), p.chunkSize())
}

func (p *Piece) numDirtyBytes(lock bool) (ret pp.Integer) {
	// defer func() {
	// 	if ret > p.length() {
	// 		panic("too many dirty bytes")
	// 	}
	// }()
	numRegularDirtyChunks := p.numDirtyChunks(lock)
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

func (p *Piece) lastChunkIndex() chunkIndexType {
	return p.numChunks() - 1
}

func (p *Piece) bytesLeft() (ret pp.Integer) {
	if p.t.pieceComplete(p.index, true) {
		return 0
	}
	return p.length() - p.numDirtyBytes(true)
}

// Forces the piece data to be rehashed.
func (p *Piece) VerifyData() {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	target := p.numVerifies + 1
	if p.hashing {
		target++
	}
	// log.Printf("target: %d", target)
	p.t.queuePieceCheck(p.index, true)
	for {
		// log.Printf("got %d verifies", p.numVerifies)
		if p.numVerifies >= target {
			break
		}
		p.t.cl.event.Wait()
	}
	// log.Print("done")
}

func (p *Piece) queuedForHash(lock bool) bool {
	return p.t.pieceQueuedForHash(p.index, lock)
}

func (p *Piece) torrentBeginOffset() int64 {
	return int64(p.index) * p.t.info.PieceLength
}

func (p *Piece) torrentEndOffset() int64 {
	return p.torrentBeginOffset() + int64(p.length())
}

func (p *Piece) SetPriority(prio piecePriority) {
	p.t.mu.Lock()
	defer p.t.mu.Unlock()
	p.priority = prio
	p.t.updatePiecePriority(p.index, "Piece.SetPriority", false)
}

func (p *Piece) purePriority(lock bool) (ret piecePriority) {
	if lock {
		p.t.mu.RLock()
		defer p.t.mu.RUnlock()
	}
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

func (p *Piece) uncachedPriority(lock bool) (ret piecePriority) {
	if p.hashing || p.marking || p.t.pieceComplete(p.index, lock) || p.queuedForHash(lock) {
		return PiecePriorityNone
	}
	return p.purePriority(lock)
}

// Tells the Client to refetch the completion status from storage, updating priority etc. if
// necessary. Might be useful if you know the state of the piece data has changed externally.
func (p *Piece) UpdateCompletion() {
	p.t.mu.Lock()
	defer p.mu.Unlock()
	p.t.updatePieceCompletion(p.index, false)
}

func (p *Piece) completion(lock bool) (ret storage.Completion) {
	ret.Complete = p.t.pieceComplete(p.index, lock)
	ret.Ok = p.storageCompletionOk
	return
}

func (p *Piece) allChunksDirty(lock bool) bool {
	return p.numDirtyChunks(lock) == p.numChunks()
}

func (p *Piece) State() PieceState {
	return p.t.PieceState(p.index)
}

func (p *Piece) requestIndexOffset() RequestIndex {
	return p.t.pieceRequestIndexOffset(p.index)
}

func (p *Piece) availability(lock bool) int {
	if lock {
		p.t.mu.RLock()
		defer p.t.mu.RUnlock()
	}
	return len(p.t.connsWithAllPieces) + p.relativeAvailability
}
