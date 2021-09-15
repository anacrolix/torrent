package request_strategy

import (
	"github.com/anacrolix/torrent/storage"
)

type Torrent struct {
	Pieces   []Piece
	Capacity storage.TorrentCapacity
	Peers    []Peer // not closed.
	// Some value that's unique and stable between runs. Could even use the infohash?
	StableId uintptr

	MaxUnverifiedBytes int64
}
