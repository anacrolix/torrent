package torrent

import (
	"math"
	"sync"
	"time"

	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/prioritybitmap"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

type requestStrategyPiece interface {
	numChunks() pp.Integer
	dirtyChunks() bitmap.Bitmap
	chunkIndexRequest(i pp.Integer) request
}

type requestStrategyTorrent interface {
	numConns() int
	numReaders() int
	numPieces() int
	readerPiecePriorities() (now, readahead bitmap.Bitmap)
	ignorePieces() bitmap.Bitmap
	pendingPieces() *prioritybitmap.PriorityBitmap
}

type requestStrategyConnection interface {
	torrent() requestStrategyTorrent
	peerPieces() bitmap.Bitmap
	pieceRequestOrder() *prioritybitmap.PriorityBitmap
	fastest() bool
	stats() *ConnStats
	totalExpectingTime() time.Duration
	peerMaxRequests() int
	chunksReceivedWhileExpecting() int64
}

type requestStrategy interface {
	iterPendingPieces(requestStrategyConnection, func(pieceIndex) bool) bool
	iterUndirtiedChunks(requestStrategyPiece, func(chunkSpec) bool) bool
	nominalMaxRequests(requestStrategyConnection) int
	shouldRequestWithoutBias(requestStrategyConnection) bool
	piecePriority(requestStrategyConnection, pieceIndex, piecePriority, int) int
	hooks() requestStrategyHooks
}

type requestStrategyHooks struct {
	sentRequest    func(request)
	deletedRequest func(request)
}

type requestStrategyCallbacks interface {
	requestTimedOut(request)
}

// Favour higher priority pieces with some fuzzing to reduce overlaps and wastage across
// connections.
type requestStrategyFuzzing struct {
	requestStrategyDefaults
}

// The fastest connection downloads strictly in order of priority, while all others adhere to their
// piece inclinations.
type requestStrategyFastest struct {
	requestStrategyDefaults
}

func newRequestStrategyMaker(rs requestStrategy) RequestStrategyMaker {
	return func(requestStrategyCallbacks, sync.Locker) requestStrategy {
		return rs
	}
}

func RequestStrategyFastest() RequestStrategyMaker {
	return newRequestStrategyMaker(requestStrategyFastest{})
}

func RequestStrategyFuzzing() RequestStrategyMaker {
	return newRequestStrategyMaker(requestStrategyFuzzing{})
}

func (requestStrategyFastest) shouldRequestWithoutBias(cn requestStrategyConnection) bool {
	if cn.torrent().numReaders() == 0 {
		return false
	}
	if cn.torrent().numConns() == 1 {
		return true
	}
	if cn.fastest() {
		return true
	}
	return false
}

// Requests are strictly by piece priority, and not duplicated until duplicateRequestTimeout is
// reached.
type requestStrategyDuplicateRequestTimeout struct {
	requestStrategyDefaults
	// How long to avoid duplicating a pending request.
	duplicateRequestTimeout time.Duration

	callbacks requestStrategyCallbacks

	// The last time we requested a chunk. Deleting the request from any connection will clear this
	// value.
	lastRequested map[request]*time.Timer
	// The lock to take when running a request timeout handler.
	timeoutLocker sync.Locker
}

type RequestStrategyMaker func(callbacks requestStrategyCallbacks, clientLocker sync.Locker) requestStrategy

func RequestStrategyDuplicateRequestTimeout(duplicateRequestTimeout time.Duration) RequestStrategyMaker {
	return func(callbacks requestStrategyCallbacks, clientLocker sync.Locker) requestStrategy {
		return requestStrategyDuplicateRequestTimeout{
			duplicateRequestTimeout: duplicateRequestTimeout,
			callbacks:               callbacks,
			lastRequested:           make(map[request]*time.Timer),
			timeoutLocker:           clientLocker,
		}
	}
}

func (rs requestStrategyDuplicateRequestTimeout) hooks() requestStrategyHooks {
	return requestStrategyHooks{
		deletedRequest: func(r request) {
			if t, ok := rs.lastRequested[r]; ok {
				t.Stop()
				delete(rs.lastRequested, r)
			}
		},
		sentRequest: rs.onSentRequest,
	}
}

func (rs requestStrategyDuplicateRequestTimeout) iterUndirtiedChunks(p requestStrategyPiece, f func(chunkSpec) bool) bool {
	for i := pp.Integer(0); i < pp.Integer(p.numChunks()); i++ {
		if p.dirtyChunks().Get(bitmap.BitIndex(i)) {
			continue
		}
		r := p.chunkIndexRequest(i)
		if rs.wouldDuplicateRecent(r) {
			continue
		}
		if !f(r.chunkSpec) {
			return false
		}
	}
	return true
}

func (requestStrategyFuzzing) piecePriority(cn requestStrategyConnection, piece pieceIndex, tpp piecePriority, prio int) int {
	switch tpp {
	case PiecePriorityNormal:
	case PiecePriorityReadahead:
		prio -= int(cn.torrent().numPieces())
	case PiecePriorityNext, PiecePriorityNow:
		prio -= 2 * int(cn.torrent().numPieces())
	default:
		panic(tpp)
	}
	prio += int(piece / 3)
	return prio
}

func (requestStrategyDuplicateRequestTimeout) iterPendingPieces(cn requestStrategyConnection, f func(pieceIndex) bool) bool {
	return iterUnbiasedPieceRequestOrder(cn, f)
}
func defaultIterPendingPieces(rs requestStrategy, cn requestStrategyConnection, f func(pieceIndex) bool) bool {
	if rs.shouldRequestWithoutBias(cn) {
		return iterUnbiasedPieceRequestOrder(cn, f)
	} else {
		return cn.pieceRequestOrder().IterTyped(func(i int) bool {
			return f(pieceIndex(i))
		})
	}
}
func (rs requestStrategyFuzzing) iterPendingPieces(cn requestStrategyConnection, cb func(pieceIndex) bool) bool {
	return defaultIterPendingPieces(rs, cn, cb)
}
func (rs requestStrategyFastest) iterPendingPieces(cn requestStrategyConnection, cb func(pieceIndex) bool) bool {
	return defaultIterPendingPieces(rs, cn, cb)
}

func (rs requestStrategyDuplicateRequestTimeout) onSentRequest(r request) {
	rs.lastRequested[r] = time.AfterFunc(rs.duplicateRequestTimeout, func() {
		rs.timeoutLocker.Lock()
		delete(rs.lastRequested, r)
		rs.timeoutLocker.Unlock()
		rs.callbacks.requestTimedOut(r)
	})
}

// The actual value to use as the maximum outbound requests.
func (rs requestStrategyDuplicateRequestTimeout) nominalMaxRequests(cn requestStrategyConnection) (ret int) {
	expectingTime := int64(cn.totalExpectingTime())
	if expectingTime == 0 {
		expectingTime = math.MaxInt64
	} else {
		expectingTime *= 2
	}
	return int(clamp(
		1,
		int64(cn.peerMaxRequests()),
		max(
			// It makes sense to always pipeline at least one connection, since latency must be
			// non-zero.
			2,
			// Request only as many as we expect to receive in the duplicateRequestTimeout
			// window. We are trying to avoid having to duplicate requests.
			cn.chunksReceivedWhileExpecting()*int64(rs.duplicateRequestTimeout)/expectingTime,
		),
	))
}
func (rs requestStrategyDuplicateRequestTimeout) wouldDuplicateRecent(r request) bool {
	// This piece has been requested on another connection, and the duplicate request timer is still
	// running.
	_, ok := rs.lastRequested[r]
	return ok
}
