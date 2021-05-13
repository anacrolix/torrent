package request_strategy

import (
	"github.com/anacrolix/torrent/types"
)

type Piece struct {
	Request           bool
	Priority          piecePriority
	Partial           bool
	Availability      int64
	Length            int64
	NumPendingChunks  int
	IterPendingChunks func(func(types.ChunkSpec))
}

func (p *Piece) iterPendingChunksWrapper(f func(ChunkSpec)) {
	i := p.IterPendingChunks
	if i != nil {
		i(f)
	}
}
