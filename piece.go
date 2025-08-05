package torrent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/storage"
)

// Why is it an int64?
type pieceVerifyCount = int64

type Piece struct {
	// The completed piece SHA1 hash, from the metainfo "pieces" field. Nil if the info is not V1
	// compatible.
	hash *metainfo.Hash
	// Not easy to use unique.Handle because we need this as a slice sometimes.
	hashV2 g.Option[[32]byte]
	t      *Torrent
	index  pieceIndex
	// First and one after the last file indexes.
	beginFile, endFile int

	readerCond chansync.BroadcastCond

	numVerifies     pieceVerifyCount
	numVerifiesCond chansync.BroadcastCond

	publicPieceState PieceState
	// Piece-specific priority. There are other priorities like File and Reader.
	priority PiecePriority
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

	// Value to twiddle to detect races.
	race byte
	// Currently being hashed.
	hashing bool
	// The piece state may have changed, and is being synchronized with storage.
	marking bool
	// The Completion.Ok field cached from the storage layer.
	storageCompletionOk bool
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
		p.requestIndexBegin(),
		p.t.pieceRequestIndexBegin(p.index+1),
	))
}

func (p *Piece) unpendChunkIndex(i chunkIndexType) {
	p.t.dirtyChunks.Add(p.requestIndexBegin() + i)
	p.t.updatePieceRequestOrderPiece(p.index)
	p.readerCond.Broadcast()
}

func (p *Piece) pendChunkIndex(i RequestIndex) {
	p.t.dirtyChunks.Remove(p.requestIndexBegin() + i)
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
	return p.t.dirtyChunks.Contains(p.requestIndexBegin() + chunk)
}

func (p *Piece) iterCleanChunks(it *roaring.IntIterator) iter.Seq[chunkIndexType] {
	return func(yield func(chunkIndexType) bool) {
		it.Initialize(&p.t.dirtyChunks.Bitmap)
		begin := uint32(p.requestIndexBegin())
		end := uint32(p.requestIndexMaxEnd())
		it.AdvanceIfNeeded(begin)
		for next := begin; next < end; next++ {
			if !it.HasNext() || it.Next() != next {
				if !yield(chunkIndexType(next - begin)) {
					return
				}
			}
		}
		return
	}
}

func (p *Piece) firstCleanChunk() (_ g.Option[chunkIndexType]) {
	for some := range p.iterCleanChunks(&p.t.cl.roaringIntIterator) {
		return g.Some(some)
	}
	return
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
func (p *Piece) VerifyData() error {
	return p.VerifyDataContext(context.Background())
}

// Forces the piece data to be rehashed. This might be a temporary method until
// an event-based one is created. Possibly this blocking style is more suited to
// external control of hashing concurrency.
func (p *Piece) VerifyDataContext(ctx context.Context) error {
	locker := p.t.cl.locker()
	locker.Lock()
	if p.t.closed.IsSet() {
		return errTorrentClosed
	}
	target, err := p.t.queuePieceCheck(p.index)
	locker.Unlock()
	if err != nil {
		return err
	}
	//log.Printf("target: %d", target)
	for {
		locker.RLock()
		done := p.numVerifies >= target
		//log.Printf("got %d verifies", p.numVerifies)
		numVerifiesChanged := p.numVerifiesCond.Signaled()
		locker.RUnlock()
		if done {
			//log.Print("done")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.t.closed.Done():
			return errTorrentClosed
		case <-numVerifiesChanged:
		}
	}
}

func (p *Piece) queuedForHash() bool {
	return p.t.piecesQueuedForHash.Contains(p.index)
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
	for _, f := range p.files() {
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
	// Ordered by cheapest checks and most likely to persist first.

	// There's a method that gets this with complete, but that requires a bitmap lookup. Defer that.
	if !p.storageCompletionOk {
		// Piece completion unknown.
		return true
	}
	if p.hashing || p.marking || !p.haveHash() || p.t.dataDownloadDisallowed.IsSet() {
		return true
	}
	// This is valid after we know that storage completion has been cached.
	if p.t.pieceComplete(p.index) {
		return true
	}
	if p.queuedForHash() {
		return true
	}
	return false
}

// This is the priority adjusted for piece state like completion, hashing etc.
func (p *Piece) effectivePriority() (ret PiecePriority) {
	if p.ignoreForRequests() {
		return PiecePriorityNone
	}
	return p.purePriority()
}

// Tells the Client to refetch the completion status from storage, updating priority etc. if
// necessary. Might be useful if you know the state of the piece data has
// changed externally. TODO: Document why this is public, maybe change name to
// SyncCompletion or something.
func (p *Piece) UpdateCompletion() {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	p.t.updatePieceCompletion(p.index)
}

// TODO: Probably don't include Completion.Err?
func (p *Piece) completion() (ret storage.Completion) {
	ret.Ok = p.storageCompletionOk
	if ret.Ok {
		ret.Complete = p.t.pieceComplete(p.index)
	}
	return
}

func (p *Piece) allChunksDirty() bool {
	return p.numDirtyChunks() == p.numChunks()
}

func (p *Piece) State() PieceState {
	return p.t.PieceState(p.index)
}

// The first possible request index for the piece.
func (p *Piece) requestIndexBegin() RequestIndex {
	return p.t.pieceRequestIndexBegin(p.index)
}

// The maximum end request index for the piece. Some of the requests might not be valid, it's for
// cleaning up arrays and bitmaps in broad strokes.
func (p *Piece) requestIndexMaxEnd() RequestIndex {
	new := min(p.t.pieceRequestIndexBegin(p.index+1), p.t.maxEndRequest())
	if false {
		old := p.t.pieceRequestIndexBegin(p.index + 1)
		panicif.NotEq(new, old)
	}
	return new
}

func (p *Piece) availability() int {
	return len(p.t.connsWithAllPieces) + p.relativeAvailability
}

// For v2 torrents, files are aligned to pieces so there should always only be a single file for a
// given piece.
func (p *Piece) mustGetOnlyFile() *File {
	panicif.NotEq(p.numFiles(), 1)
	return (*p.t.files)[p.beginFile]
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
	return p.numFiles() == 1 && p.mustGetOnlyFile().length > p.t.info.PieceLength
}

// TODO: This looks inefficient. It will rehash everytime it is called. The hashes should be
// generated once.
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
	if c := storage.Completion(); c.Ok && !c.Complete {
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

func (p *Piece) files() iter.Seq2[int, *File] {
	return func(yield func(int, *File) bool) {
		for i := p.beginFile; i < p.endFile; i++ {
			if !yield(i, (*p.t.files)[i]) {
				return
			}
		}
	}
}

func (p *Piece) numFiles() int {
	return p.endFile - p.beginFile
}

func (p *Piece) hasActivePeerConnRequests() (ret bool) {
	for ri := p.requestIndexBegin(); ri < p.requestIndexMaxEnd(); ri++ {
		_, ok := p.t.requestState[ri]
		ret = ret || ok
	}
	return
}

// The value of numVerifies after the next hashing operation that hasn't yet begun.
func (p *Piece) nextNovelHashCount() (ret pieceVerifyCount) {
	ret = p.numVerifies + 1
	if p.hashing {
		// The next novel hash will be the one after the current one.
		ret++
	}
	return
}

// Here so it's zero-arity and we can use it in DeferOnce.
func (p *Piece) publishStateChange() {
	t := p.t
	cur := t.pieceState(p.index)
	if cur != p.publicPieceState {
		p.publicPieceState = cur
		t.pieceStateChanges.Publish(PieceStateChange{
			p.index,
			cur,
		})
	}
}

func (p *Piece) fileExtents(offsetIntoPiece int64) iter.Seq2[int, segments.Extent] {
	return p.t.fileSegmentsIndex.Unwrap().LocateIter(segments.Extent{
		p.torrentBeginOffset() + offsetIntoPiece,
		int64(p.length()) - offsetIntoPiece,
	})
}
