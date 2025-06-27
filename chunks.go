package torrent

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/metainfo"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/langx"
	"github.com/james-lawrence/torrent/internal/x/bitmapx"
)

// empty error signifies that the queue is empty.
type empty struct {
	Outstanding int
	Missing     int
}

func (t empty) Error() string {
	return fmt.Sprintf("empty queue: outstanding requests(%d) - missing requests(%d)", t.Outstanding, t.Missing)
}

func chunksPerPiece(plength, clength int64) int64 {
	return int64(math.Ceil(float64(plength) / float64(clength)))
}

func numChunks(total, plength, clength int64) int64 {
	if total == 0 || plength == 0 {
		return 0
	}

	npieces := total / plength // golang floors integer division.
	remainder := total - (npieces * plength)
	chunksper := chunksPerPiece(plength, clength)
	rchunks := int64(math.Ceil(float64(remainder) / float64(clength)))
	return (chunksper * npieces) + rchunks
}

func chunkOffset(cidx, plength, clength int64) int64 {
	cidx = cidx % chunksPerPiece(plength, clength)
	return cidx * clength
}

func chunkLength(total, cidx, plength, clength int64, maximum bool) int64 {
	chunksper := chunksPerPiece(plength, clength)

	maxlength := min(clength, plength)

	if maximum {
		max := total % plength
		if max == 0 {
			max = plength
		}

		return max - ((cidx % chunksper) * maxlength)
	}

	if cidx%chunksper == chunksper-1 && plength%clength > 0 {
		return plength % clength
	}

	return maxlength
}

func pindex(chunk, plength, clength int64) int64 {
	return chunk / chunksPerPiece(plength, clength)
}

type chunkopt func(*chunks)

func chunkoptCond(cond *sync.Cond) chunkopt {
	return func(c *chunks) {
		c.cond = cond
	}
}

func chunkoptCompleted(completed *roaring.Bitmap) chunkopt {
	return func(c *chunks) {
		c.completed = completed
	}
}

func newChunks(clength uint64, m *metainfo.Info, options ...chunkopt) *chunks {
	if clength == 0 {
		panic("chunksize cannot be zero")
	}

	p := langx.Autoptr(langx.Clone(chunks{
		cond:        sync.NewCond(&sync.Mutex{}),
		mu:          &sync.RWMutex{},
		meta:        m,
		pieces:      uint64(m.NumPieces()),
		cmaximum:    numChunks(m.TotalLength(), m.PieceLength, int64(clength)),
		clength:     int64(clength),
		gracePeriod: 2 * time.Minute,
		outstanding: make(map[uint64]request),
		missing:     roaring.NewBitmap(),
		unverified:  roaring.NewBitmap(),
		failed:      roaring.NewBitmap(),
		completed:   roaring.NewBitmap(),
		pool: &sync.Pool{
			New: func() interface{} {
				b := make([]byte, clength)
				return &b
			},
		},
	}, options...))

	// log.Printf("%p - TOTAL LENGTH %d LENGTH %d NUMCHUNKS %d - CHUNK LENGTH %d - PIECE LEGNTH %d\n", p, p.meta.TotalLength(), p.meta.Length, p.cmaximum, p.clength, p.meta.PieceLength)
	return p
}

// chunks manages retrieving specific chunks of the torrent. its concurrent safe,
// and automatically recovers chunks that were requested but not received.
// the goal here is to have a single source of truth for what chunks are outstanding.
type chunks struct {
	cond *sync.Cond
	meta *metainfo.Info

	pieces uint64

	// chunk length
	clength int64
	// maximum valid chunk index.
	cmaximum int64

	// track the number of reaping requests.
	reapers int64

	// gracePeriod how long to wait before reaping outstanding requests.
	gracePeriod time.Duration

	// cache of chunks we're missing.
	missing *roaring.Bitmap

	// cache of chunks that failed the digest process. this bitmap is used to force
	// connections to kill themselves when a digest fails validation.
	failed *roaring.Bitmap

	// The last time we requested a chunk. Deleting the request from any
	// connection will clear this value.
	outstanding map[uint64]request

	// cache of the pieces that need to be verified.
	unverified *roaring.Bitmap

	// cache of completed piece indices, this means they have been retrieved and verified.
	completed *roaring.Bitmap

	// next time to reap the outstanding requests
	nextReap time.Time

	// buffer pool for storing chunks
	pool *sync.Pool

	mu *sync.RWMutex
}

func (t *chunks) reap(window time.Duration) {
	ts := time.Now()
	recovered := 0
	scanned := 0

	if len(t.outstanding) == 0 {
		return
	}

	if t.nextReap.After(ts) {
		return
	}

	for _, req := range t.outstanding {
		scanned++

		if deadline := req.Reserved.Add(t.gracePeriod); deadline.Before(ts) {
			t.retry(req)
			recovered++
		} else if t.nextReap.IsZero() || t.nextReap.Before(deadline) {
			// by saving the deadline that will expire the furthest into the future
			// we ensure we'll capture all the expired requests between now and then.
			t.nextReap = deadline.Add(time.Millisecond)
		}

		// check after its scanned a few elements.
		// the cap is somewhat arbitrary based on testing.
		// thousands of items can be scanned in < millisecond times.
		// the time.Since method vastly outweighs the scanning.
		if scanned%1000 == 0 && time.Since(ts) > window {
			break
		}
	}

	// if recovered > 0 {
	// 	log.Println(recovered, "/", scanned, "recovered in", time.Since(ts), ">", window, t.gracePeriod, "remaining", len(t.outstanding), "next reap", t.nextReap)
	// 	log.Printf("remaining(%d) - failed(%d) - outstanding(%d) - unverified(%d) - completed(%d)\r\n", t.missing.Len(), t.failed.GetCardinality(), len(t.outstanding), t.unverified.Len(), t.completed.Len())
	// }
}

// chunks returns the set of chunk id's for the given piece.
// deprecated: use Range.
func (t *chunks) chunks(pid uint64) (cidxs []int) {
	for cidx, cidn := t.Range(pid); cidx < cidn; cidx++ {
		cidxs = append(cidxs, int(cidx))
	}

	return cidxs
}

// returns the range of chunks for the given piece id.
func (t *chunks) Range(pid uint64) (_min, _max uint64) {
	cpp := chunksPerPiece(t.meta.PieceLength, t.clength)
	cid0 := pid * uint64(cpp)
	cidn := min(cid0+uint64(cpp), uint64(t.cmaximum))
	// log.Println("range calc", t.meta.PieceLength, t.clength, cpp, t.cmaximum, "->", cid0, cidn, math.MaxUint32)
	return cid0, cidn
}

func (t *chunks) lastChunk(pid int) int {
	cpp := chunksPerPiece(t.meta.PieceLength, t.clength)
	chunks := numChunks(t.meta.PieceLength, t.meta.PieceLength, t.clength)
	return (pid * int(cpp)) + int(chunks-1)
}

func (t *chunks) request(cidx int64, prio int) (r request, err error) {
	if t.cmaximum <= cidx {
		return r, fmt.Errorf("chunk index out of range: %d - %d", cidx, t.cmaximum)
	}

	pidx := pindex(cidx, t.meta.PieceLength, t.clength)
	start := chunkOffset(cidx, t.meta.PieceLength, t.clength)
	length := chunkLength(t.meta.TotalLength(), cidx, t.meta.PieceLength, t.clength, cidx == t.cmaximum-1)
	return newRequest2(pp.Integer(pidx), pp.Integer(start), pp.Integer(length), prio), nil
}

func (t *chunks) requestCID(r request) int {
	return int((chunksPerPiece(t.meta.PieceLength, t.clength) * int64(r.Index)) + int64(r.Begin)/t.clength)
}

func (t *chunks) pindex(cidx int) int {
	return int(pindex(int64(cidx), t.meta.PieceLength, t.clength))
}

func (t *chunks) fill(b *roaring.Bitmap) *roaring.Bitmap {
	t.mu.Lock()
	defer t.mu.Unlock()
	b.AddRange(0, uint64(t.cmaximum))
	return b
}

func (t *chunks) peek(available *roaring.Bitmap) (cidx int, req request, err error) {
	union := available.Clone()
	union.And(t.missing)

	if union.IsEmpty() {
		return cidx, req, empty{Outstanding: len(t.outstanding), Missing: int(t.missing.GetCardinality())}
	}

	cidx = int(union.Minimum())
	if req, err = t.request(int64(cidx), int(-1*(cidx+1))); err != nil {
		return cidx, req, errorsx.Wrap(err, "invalid request")
	}

	return cidx, req, nil
}

// ChunksMissing checks if the given piece has any missing chunks.
func (t *chunks) ChunksMissing(pid uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return bitmapx.Range(t.Range(pid)).AndCardinality(t.missing) > 0
}

func (t *chunks) MergeInto(d, m *roaring.Bitmap) {
	t.mu.Lock()
	defer t.mu.Unlock()

	d.Or(m)
}

func (t *chunks) InitFromUnverified(m *roaring.Bitmap) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.unverified.Or(m)
	t.missing = bitmapx.Fill(uint64(t.cmaximum))
	t.missing.AndNot(t.unverified)
}

func (t *chunks) Intersects(a, b *roaring.Bitmap) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return a.Intersects(b)
}

// used to clone bitmaps owned by chunks safely
func (t *chunks) Clone(a *roaring.Bitmap) *roaring.Bitmap {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return a.Clone()
}

// ChunksAvailable returns true iff all the chunks for the given piece are awaiting
// digesting.
func (t *chunks) ChunksAvailable(pid uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cid0, cidn := t.Range(pid)
	return bitmapx.Range(cid0, cidn).AndCardinality(t.unverified) == cidn-cid0
}

// ChunksHashing return true iff any chunk for the given piece has been marked as unverified.
func (t *chunks) ChunksHashing(pid uint64) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return bitmapx.Range(t.Range(pid)).AndCardinality(t.unverified) > 0
}

// ChunksComplete returns true iff all the chunks for the given piece has been marked as completed.
func (t *chunks) ChunksComplete(pid uint64) (b bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.completed.ContainsInt(int(pid))
}

func (t *chunks) ChunksReadable(pid uint64) (b bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.completed.ContainsInt(int(pid)) || bitmapx.Range(t.Range(pid)).AndCardinality(t.unverified) > 0
}

// returns the number of bytes allowed to read for the given offset.
// 0 is acceptable. -1 means read is blocked.
func (t *chunks) DataAvailableForOffset(offset int64) (allowed int64) {
	if t.meta.PieceLength == 0 {
		return 0
	}

	pid := uint64(t.meta.OffsetToIndex(offset))
	if !t.ChunksComplete(pid) {
		return -1
	}

	for i := pid + 1; t.ChunksComplete(i); i++ {
		pid++
	}

	endoffset := (int64(pid) * t.meta.PieceLength) + t.meta.PieceLength

	return endoffset - offset
}

// Chunks returns the chunk requests for the given piece.
func (t *chunks) chunksRequests(idx uint64) (requests []request) {
	for cidx, cidn := t.Range(idx); cidx < cidn; cidx++ {
		req, _ := t.request(int64(cidx), -1*int(cidx+1))
		requests = append(requests, req)
	}

	return requests
}

func (t *chunks) ChunksAdjust(pid uint64) (changed bool) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.completed.ContainsInt(int(pid)) {
		return false
	}

	for cidx, cidn := t.Range(pid); cidx < cidn; cidx++ {
		tmp := t.missing.CheckedAdd(uint32(cidx))
		if tmp {
			t.unverified.Remove(uint32(cidx))
		}
		// log.Output(2, fmt.Sprintf("%p CHUNK PRIORITY ADJUSTED: %d %s prios %d %d %t %d\n", t, c, fmt.Sprintf("(%d)", pid), oprio, prio, tmp, t.missing.Len()))
		changed = changed || tmp
	}

	return changed
}

// ChunksPend marks all the chunks for the given piece to the priority.
// returns true if any changes were made.
func (t *chunks) ChunksPend(idx uint64) (changed bool) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, c := range t.chunksRequests(idx) {
		tmp := t.pend(c)
		changed = changed || tmp
	}

	return changed
}

// ChunksRelease releases all the chunks for the given piece back into the missing
// pool.
func (t *chunks) ChunksRelease(idx uint64) (changed bool) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, c := range t.chunksRequests(idx) {
		tmp := t.release(c)
		changed = changed || tmp
	}

	return changed
}

// ChunksRetry releases all the chunks for the given piece back into the missing
// pool.
func (t *chunks) ChunksRetry(idx uint64) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, c := range t.chunksRequests(idx) {
		t.retry(c)
	}
}

// Chunks returns the chunk requests for the given piece.
func (t *chunks) Chunks(idx uint64) (requests []request) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.chunksRequests(idx)
}

// Priority returns the priority of the first chunk based on the piece ID.
func (t *chunks) Priority(idx int) int {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	cidx := chunksPerPiece(t.meta.PieceLength, t.cmaximum) * int64(idx)

	if t.missing.ContainsInt(int(cidx)) {
		return int(-1 * (cidx + 1))
	}

	return int(0)
}

// Peek at the request based on availability.
func (t *chunks) Peek(available *roaring.Bitmap) (req request, err error) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, req, err = t.peek(available)
	return req, err
}

// Pop the next piece to request. this advances a chunk from missing to oustanding.
//
// TODO: it'd be nice to use available as the bitmap to pop from to avoid repeatedly
// having to scan from start to finish the missing bitmap on torrent that contain pieces
// that are near the end of the local priority list. which slows down all connections.
//
// instead we could Pop off the available bitmap and mark it in both bitmaps as outstanding.
// the outstanding request could track which available bitmap it belongs to.
// but this complicates some of the logic, and I'm leaving it to do once refactoring
// the locks and contention issues are resolved.
func (t *chunks) Pop(n int, available *roaring.Bitmap) (reqs []request, err error) {
	if n <= 0 { // sanity check.
		return reqs, nil
	}

	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	t.recover()

	reqs = make([]request, 0, n)
	for i := 0; i < n; i++ {
		var (
			cidx int
			req  request
		)

		if cidx, req, err = t.peek(available); err != nil {
			// log.Println("Popped empty", i, "<", n, ",", err)
			return reqs, err
		}

		t.outstanding[req.Digest] = req
		t.missing.Remove(uint32(cidx))

		reqs = append(reqs, req)

		// log.Output(2, fmt.Sprintf("(%d) c(%p) popped: d(%020d - %d) r(%d,%d,%d) - %t\n", os.Getpid(), t, req.Digest, cidx, req.Index, req.Begin, req.Length, t.missing.Contains(cidx)))
	}

	return reqs, nil
}

// Recover initiate a collection of outstanding requests.
// this moves them back into the missing bitmap, allowing them to be requested again.
func (t *chunks) recover() {
	if atomic.CompareAndSwapInt64(&t.reapers, 0, 1) {
		// log.Println("reaping initiated")
		t.reap(10 * time.Millisecond)
		// log.Println("reaping completed")
		atomic.CompareAndSwapInt64(&t.reapers, 1, 0)
	}
}

func (t *chunks) retry(r request) {
	cidx := t.requestCID(r)

	// _, ok := t.outstanding[r.Digest]
	// log.Output(3, fmt.Sprintf("c(%p) retry request: d(%020d - %d) r(%d,%d,%d) removed(%t)", t, r.Digest, cidx, r.Index, r.Begin, r.Length, ok))

	delete(t.outstanding, r.Digest)
	t.missing.AddInt(cidx)

}

func (t *chunks) release(r request) bool {
	_, ok := t.outstanding[r.Digest]
	delete(t.outstanding, r.Digest)
	// log.Output(3, fmt.Sprintf("c(%p) release request: d(%020d) r(%d,%d,%d)", t, r.Digest, r.Index, r.Begin, r.Length))
	return ok
}

func (t *chunks) pend(r request) (changed bool) {
	cidx := t.requestCID(r)

	// unconditionally mark the chunk as missing.
	changed = t.missing.CheckedAdd(uint32(cidx))

	// remove from unverified.
	t.unverified.Remove(uint32(cidx))

	delete(t.outstanding, r.Digest)

	// log.Output(3, fmt.Sprintf("c(%p) pending request: d(%020d - %d) r(%d,%d,%d) (%d) u(%t)", t, r.Digest, cidx, r.Index, r.Begin, r.Length, prio, changed))

	return changed
}

func (t *chunks) Cardinality(a *roaring.Bitmap) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int(a.GetCardinality())
}

// returns number of pieces that are readable.
func (t *chunks) Readable() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cpp := chunksPerPiece(t.meta.PieceLength, t.clength)
	return t.unverified.GetCardinality() + min((uint64(cpp)*t.completed.GetCardinality()), t.pieces)
}

func (t *chunks) ReadableBitmap() *roaring.Bitmap {
	t.mu.RLock()
	defer t.mu.RUnlock()

	bm := t.unverified.Clone()
	t.completed.Iterate(func(x uint32) bool {
		bm.AddRange(t.Range(uint64(x)))
		return true
	})

	return bm
}

// returns true if any chunks are in incomplete states (missing, oustanding, unverified)
func (t *chunks) Incomplete() bool {
	if t == nil {
		panic("chunks should never be nil for chunks missings")
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return (int(t.missing.GetCardinality()) + len(t.outstanding) + int(t.unverified.GetCardinality())) > 0
}

func (t *chunks) Snapshot(s *Stats) *Stats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s.Missing = int(t.missing.GetCardinality())
	s.Outstanding = len(t.outstanding)
	s.Unverified = int(t.unverified.GetCardinality())
	s.Failed = int(t.failed.GetCardinality())
	s.Completed = int(t.completed.GetCardinality())
	return s
}

// FailuresReset - used to clear failures
func (t *chunks) FailuresReset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failed.Clear()
}

// Outstanding returns a copy of the outstanding requests
func (t *chunks) Outstanding() (dup map[uint64]request) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.RLock()
	defer t.mu.RUnlock()

	dup = make(map[uint64]request, len(t.outstanding))
	for i, r := range t.outstanding {
		dup[i] = r
	}
	return dup
}

// Pend forces a chunks to be added to the missing queue.
func (t *chunks) Pend(reqs ...request) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.recover()
	for _, r := range reqs {
		t.pend(r)
	}
}

// Release a request from the outstanding mapping.
func (t *chunks) Release(reqs ...request) (b bool) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.recover()

	b = true
	for _, r := range reqs {
		b = b && t.release(r)
		// cidx := t.requestCID(r)
		// log.Output(2, fmt.Sprintf("c(%p) released request: d(%020d - %d) r(%d,%d,%d)", t, r.Digest, cidx, r.Index, r.Begin, r.Length))
	}

	return b
}

// Retry mark the requests to be retried.
func (t *chunks) Retry(reqs ...request) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.recover()

	for _, r := range reqs {
		t.retry(r)
	}
}

func (t *chunks) Available(r request) bool {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.unverified.ContainsInt(t.requestCID(r)) || t.completed.ContainsInt(int(r.Index))
}

// Verify mark the chunk for verification.
func (t *chunks) Verify(r request) (err error) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	cid := t.requestCID(r)

	delete(t.outstanding, r.Digest)
	t.missing.Remove(uint32(cid))
	t.unverified.AddInt(cid)

	// log.Printf("c(%p) marked for verification: d(%020d - %d) i(%d) b(%d) l(%d)\n", t, r.Digest, cid, r.Index, r.Begin, r.Length)

	return nil
}

// Validate mark all the chunks of the given piece to be validated.
func (t *chunks) Validate(pid uint64) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	t.unverified.AddRange(t.Range(pid))
}

func (t *chunks) Hashed(pid uint64, cause error) {
	if t == nil {
		panic("chunks should never be nil for hashed function call")
	}

	if cause == nil {
		t.Complete(pid)
		return
	}

	t.ChunksFailed(pid)
}

func (t *chunks) Complete(pid uint64) (changed bool) {
	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, r := range t.chunksRequests(pid) {
		cidx := t.requestCID(r)
		delete(t.outstanding, r.Digest)
		tmp := t.missing.CheckedRemove(uint32(cidx))
		tmp = t.unverified.CheckedRemove(uint32(cidx)) || tmp
		changed = changed || tmp

		// log.Output(2, fmt.Sprintf("c(%p) marked completed: (%020d - %d) r(%d,%d,%d)\n", t, r.Digest, cidx, r.Index, r.Begin, r.Length))
	}

	t.completed.AddInt(int(pid))
	t.cond.Broadcast() // wake anything waiting on completions
	return changed
}

// Failed returns the union of the current failures and the provided completed mapping.
func (t *chunks) Failed(touched *roaring.Bitmap) *roaring.Bitmap {
	if touched.IsEmpty() {
		return bitmapx.Lazy(nil)
	}

	// trace(fmt.Sprintf("initiated: %p", t.mu.(*DebugLock).m))
	// defer trace(fmt.Sprintf("completed: %p", t.mu.(*DebugLock).m))
	t.mu.RLock()
	union := t.failed.Clone()
	t.mu.RUnlock()

	if union.IsEmpty() {
		return bitmapx.Lazy(nil)
	}
	// log.Output(2, fmt.Sprintf("c(%p) checking failed chunks: %s %s\n", t, bitmapx.Debug(touched), bitmapx.Debug(t.failed)))
	union.And(touched)

	t.mu.Lock()
	t.failed.AndNot(union)
	t.mu.Unlock()
	return union
}

// ChunksFailed mark a piece by index as failed.
func (t *chunks) ChunksFailed(pid uint64) {
	if t == nil {
		panic("chunks should never be nil for chunks failed")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.failed.AddRange(t.Range(pid))
}
