package torrent

import (
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

type requestStrategyDefaults struct{}

func (requestStrategyDefaults) hooks() requestStrategyHooks {
	return requestStrategyHooks{
		sentRequest:    func(request) {},
		deletedRequest: func(request) {},
	}
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

func (requestStrategyFastest) ShouldRequestWithoutBias(cn requestStrategyConnection) bool {
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
