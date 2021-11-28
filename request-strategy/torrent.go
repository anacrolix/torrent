package request_strategy

import (
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type Torrent struct {
	Pieces   []Piece
	Capacity storage.TorrentCapacity
	// Some value that's unique and stable between runs.
	InfoHash       metainfo.Hash
	ChunksPerPiece uint32

	MaxUnverifiedBytes int64
}
