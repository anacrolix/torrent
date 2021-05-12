package request_strategy

import (
	"time"
	"unsafe"
)

type PeerNextRequestState struct {
	Interested bool
	Requests   map[Request]struct{}
}

type PeerPointer = unsafe.Pointer

type Peer struct {
	HasPiece           func(pieceIndex) bool
	MaxRequests        func() int
	HasExistingRequest func(Request) bool
	Choking            bool
	PieceAllowedFast   func(pieceIndex) bool
	DownloadRate       float64
	Age                time.Duration
	Id                 PeerPointer
}

// TODO: This might be used in more places I think.
func (p *Peer) canRequestPiece(i pieceIndex) bool {
	return p.HasPiece(i) && (!p.Choking || p.PieceAllowedFast(i))
}
