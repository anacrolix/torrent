package torrent

import (
	"errors"
	"fmt"
	"sync"

	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

type Piece struct {
	// The completed piece SHA1 hash, from the metainfo "pieces" field. Nil if the info is not V1
	// compatible.
	hash   *metainfo.Hash
	hashV2 g.Option[[32]byte]
	t      *Torrent
	index  pieceIndex
	files  []*File

	readerCond chansync.BroadcastCond

	numVerifies         int64
	hashing             bool
	marking             bool
	storageCompletionOk bool

	publicPieceState PieceState
	priority         PiecePriority
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
	return fmt.Sprintf("%s/%d", p.t.canonicalShortInfohash().HexString(), p.index)
}

func (p *Piece) Info() metainfo.Piece {
	return p.t.info.Piece(p.index)
}

func (p *Piece) Storage() storage.Piece {
	var pieceHash g.Option[[]byte]
	if p.hash != nil {
		pieceHash.Set(p.hash.Bytes())
	} else if !p.hasPieceLayer() {
		pieceHash.Set(p.mustGetOnlyFile().piecesRoot.UnwrapPtr()[:])
	} else if p.hashV2.Ok {
		pieceHash.Set(p.hashV2.Value[:])
	}
	return p.t.storage.PieceWithHash(p.Info(), pieceHash)
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

func (p *Piece) hasDirtyChunks() bool {
	return p.numDirtyChunks() != 0
}

func (p *Piece) numDirtyChunks() chunkIndexType {
	return chunkIndexType(roaringBitmapRangeCardinality[RequestIndex](
		&p.t.dirtyChunks,
		p.requestIndexOffset(),
		p.t.pieceRequestIndexOffset(p.index+1)))
}

func (p *Piece) unpendChunkIndex(i chunkIndexType) {
	p.t.dirtyChunks.Add(p.requestIndexOffset() + i)
	p.t.updatePieceRequestOrderPiece(p.index)
	p.readerCond.Broadcast()
}

func (p *Piece) pendChunkIndex(i RequestIndex) {
	p.t.dirtyChunks.Remove(p.requestIndexOffset() + i)
	p.t.updatePieceRequestOrderPiece(p.index)
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
	return p.t.dirtyChunks.Contains(p.requestIndexOffset() + chunk)
}

func (p *Piece) chunkIndexSpec(chunk chunkIndexType) ChunkSpec {
	return chunkIndexSpec(pp.Integer(chunk), p.length(), p.chunkSize())
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

func (p *Piece) lastChunkIndex() chunkIndexType {
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
	// log.Printf("target: %d", target)
	p.t.queuePieceCheck(p.index)
	for {
		// log.Printf("got %d verifies", p.numVerifies)
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
	return p.torrentBeginOffset() + int64(p.t.usualPieceSize())
}

func (p *Piece) SetPriority(prio PiecePriority) {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	p.priority = prio
	p.t.updatePiecePriority(p.index, "Piece.SetPriority")
}

// This is priority based only on piece, file and reader priorities.
func (p *Piece) purePriority() (ret PiecePriority) {
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

func (p *Piece) ignoreForRequests() bool {
	return p.hashing || p.marking || !p.haveHash() || p.t.pieceComplete(p.index) || p.queuedForHash()
}

// This is the priority adjusted for piece state like completion, hashing etc.
func (p *Piece) effectivePriority() (ret PiecePriority) {
	if p.ignoreForRequests() {
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
	return p.numDirtyChunks() == p.numChunks()
}

func (p *Piece) State() PieceState {
	return p.t.PieceState(p.index)
}

func (p *Piece) requestIndexOffset() RequestIndex {
	return p.t.pieceRequestIndexOffset(p.index)
}

func (p *Piece) availability() int {
	return len(p.t.connsWithAllPieces) + p.relativeAvailability
}

// For v2 torrents, files are aligned to pieces so there should always only be a single file for a
// given piece.
func (p *Piece) mustGetOnlyFile() *File {
	if len(p.files) != 1 {
		panic(len(p.files))
	}
	return p.files[0]
}

// Sets the v2 piece hash, queuing initial piece checks if appropriate.
func (p *Piece) setV2Hash(v2h [32]byte) {
	// See Torrent.onSetInfo. We want to trigger an initial check if appropriate, if we didn't yet
	// have a piece hash (can occur with v2 when we don't start with piece layers).
	p.t.storageLock.Lock()
	oldV2Hash := p.hashV2.Set(v2h)
	p.t.storageLock.Unlock()
	if !oldV2Hash.Ok && p.hash == nil {
		p.t.updatePieceCompletion(p.index)
		p.t.queueInitialPieceCheck(p.index)
	}
}

// Can't do certain things if we don't know the piece hash.
func (p *Piece) haveHash() bool {
	if p.hash != nil {
		return true
	}
	if !p.hasPieceLayer() {
		return true
	}
	return p.hashV2.Ok
}

func (p *Piece) hasPieceLayer() bool {
	return len(p.files) == 1 && p.files[0].length > p.t.info.PieceLength
}

func (p *Piece) obtainHashV2() (hash [32]byte, err error) {
	if p.hashV2.Ok {
		hash = p.hashV2.Value
		return
	}
	if !p.hasPieceLayer() {
		hash = p.mustGetOnlyFile().piecesRoot.Unwrap()
		return
	}
	storage := p.Storage()
	if !storage.Completion().Complete {
		err = errors.New("piece incomplete")
		return
	}

	h := merkle.NewHash()
	if _, err = storage.WriteTo(h); err != nil {
		return
	}
	h.SumMinLength(hash[:0], int(p.t.info.PieceLength))
	return
}
