package request_strategy

type Torrent struct {
	Pieces   []Piece
	Capacity *func() *int64
	Peers    []Peer // not closed.
	// Some value that's unique and stable between runs. Could even use the infohash?
	StableId uintptr

	MaxUnverifiedBytes int64
}
