package requestStrategy

type Torrent interface {
	IgnorePiece(int) bool
	PieceLength() int64
}
