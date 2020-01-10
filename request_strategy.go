package torrent

import (
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
type requestStrategyOne struct{}

// The fastest connection downloads strictly in order of priority, while all others adhere to their
// piece inclinations.
type requestStrategyTwo struct{}

func (requestStrategyTwo) ShouldRequestWithoutBias(cn requestStrategyConnection) bool {
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
type requestStrategyThree struct {
	// How long to avoid duplicating a pending request.
	duplicateRequestTimeout time.Duration
	// The last time we requested a chunk. Deleting the request from any connection will clear this
	// value.
	lastRequested map[request]*time.Timer
	callbacks     requestStrategyCallbacks
}

type requestStrategyMaker func(callbacks requestStrategyCallbacks) requestStrategy

func requestStrategyThreeMaker(duplicateRequestTimeout time.Duration) requestStrategyMaker {
	return func(callbacks requestStrategyCallbacks) requestStrategy {
		return requestStrategyThree{
			duplicateRequestTimeout: duplicateRequestTimeout,
			callbacks:               callbacks,
			lastRequested:           make(map[request]*time.Timer),
		}
	}
}

func (rs requestStrategyThree) hooks() requestStrategyHooks {
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

func (rs requestStrategyOne) hooks() requestStrategyHooks {
	return requestStrategyHooks{}
}

func (rs requestStrategyTwo) hooks() requestStrategyHooks {
	return requestStrategyHooks{}
}
