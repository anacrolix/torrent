package request_strategy

type Torrent interface {
	IgnorePiece(int) bool
	ChunksPerPiece() uint32
	PieceLength() int64
}
