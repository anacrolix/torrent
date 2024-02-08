package requestStrategy

type Torrent interface {
	Piece(int) Piece
	PieceLength() int64
}
