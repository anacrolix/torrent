package request_strategy

import (
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type Torrent struct {
	Pieces   []Piece
	Capacity storage.TorrentCapacity
	// Unclosed Peers. Not necessary for getting requestable piece ordering.
	Peers []Peer
	// Some value that's unique and stable between runs. Could even use the infohash?
	InfoHash metainfo.Hash

	MaxUnverifiedBytes int64
}
