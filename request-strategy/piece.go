package requestStrategy

type Piece interface {
	Request() bool
	NumPendingChunks() int
}
