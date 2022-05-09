package request_strategy

type Torrent interface {
	IgnorePiece(int) bool
	PieceLength() int64
}
