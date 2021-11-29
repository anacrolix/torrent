package request_strategy

import (
	"github.com/anacrolix/torrent/metainfo"
)

type Torrent struct {
	Pieces []Piece
	// Some value that's unique and stable between runs.
	InfoHash       metainfo.Hash
	ChunksPerPiece uint32
	// TODO: This isn't actually configurable anywhere yet.
	MaxUnverifiedBytes int64
}
