package requestStrategy

type Torrent interface {
	Piece(int, bool) Piece
	PieceLength() int64
	GetPieceRequestOrder() *PieceRequestOrder
	RLock()
	RUnlock()
}
