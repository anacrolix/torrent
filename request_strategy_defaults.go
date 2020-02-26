package torrent

import (
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/bitmap"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Provides default implementations for requestStrategy methods. Could be embedded, or delegated to.
type requestStrategyDefaults struct{}

func (requestStrategyDefaults) hooks() requestStrategyHooks {
	return requestStrategyHooks{
		sentRequest:    func(request) {},
		deletedRequest: func(request) {},
	}
}

func (requestStrategyDefaults) iterUndirtiedChunks(p requestStrategyPiece, f func(chunkSpec) bool) bool {
	chunkIndices := p.dirtyChunks().Copy()
	chunkIndices.FlipRange(0, bitmap.BitIndex(p.numChunks()))
	return iter.ForPerm(chunkIndices.Len(), func(i int) bool {
		ci, err := chunkIndices.RB.Select(uint32(i))
		if err != nil {
			panic(err)
		}
		return f(p.chunkIndexRequest(pp.Integer(ci)).chunkSpec)
	})
}

func (requestStrategyDefaults) nominalMaxRequests(cn requestStrategyConnection) int {
	return int(
		max(64,
			cn.stats().ChunksReadUseful.Int64()-(cn.stats().ChunksRead.Int64()-cn.stats().ChunksReadUseful.Int64())))
}

func (requestStrategyDefaults) piecePriority(cn requestStrategyConnection, piece pieceIndex, tpp piecePriority, prio int) int {
	return prio
}

func (requestStrategyDefaults) shouldRequestWithoutBias(cn requestStrategyConnection) bool {
	return false
}
