package requestStrategy

type Piece interface {
	Request(lockTorrent bool) bool
	NumPendingChunks(lockTorrent bool) int
}
