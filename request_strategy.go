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
	chunkIndexSpec(i pp.Integer) chunkSpec
	wouldDuplicateRecent(chunkSpec) bool
}

type requestStrategyTorrent interface {
	numConns() int
	numReaders() int
	numPieces() int
	readerPiecePriorities() (now, readahead bitmap.Bitmap)
	ignorePieces() bitmap.Bitmap
	pendingPieces() *prioritybitmap.PriorityBitmap
	duplicateRequestTimeout() time.Duration
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
}
