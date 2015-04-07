package torrent

import (
	"math/rand"
	"sync"
)

type piecePriority byte

const (
	piecePriorityNone piecePriority = iota
	piecePriorityNormal
	piecePriorityReadahead
	piecePriorityNext
	piecePriorityNow
)

type piece struct {
	Hash              pieceSum
	PendingChunkSpecs map[chunkSpec]struct{}
	Hashing           bool
	QueuedForHash     bool
	EverHashed        bool
	Event             sync.Cond
	Priority          piecePriority
}

func (p *piece) shuffledPendingChunkSpecs() (css []chunkSpec) {
	if len(p.PendingChunkSpecs) == 0 {
		return
	}
	css = make([]chunkSpec, 0, len(p.PendingChunkSpecs))
	for cs := range p.PendingChunkSpecs {
		css = append(css, cs)
	}
	if len(css) <= 1 {
		return
	}
	for i := range css {
		j := rand.Intn(i + 1)
		css[i], css[j] = css[j], css[i]
	}
	return
}
