package requestStrategy

type Torrent interface {
	Piece(int, bool) Piece
	PieceLength() int64
	NumPieces() int
	GetPieceRequestOrder() *PieceRequestOrder
	RLock()
	RUnlock()
}
