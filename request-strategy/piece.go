package request_strategy

type ChunksIter func(func(ChunkIndex))

type Piece struct {
	Request           bool
	Priority          piecePriority
	Partial           bool
	Availability      int64
	Length            int64
	NumPendingChunks  int
	IterPendingChunks ChunksIter
}

func (p Piece) iterPendingChunksWrapper(f func(ChunkIndex)) {
	i := p.IterPendingChunks
	if i != nil {
		i(f)
	}
}
