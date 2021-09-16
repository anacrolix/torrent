package request_strategy

import (
	"time"
)

type PeerNextRequestState struct {
	Interested bool
	Requests   map[Request]struct{}
}

type PeerId interface {
	Uintptr() uintptr
}

type Peer struct {
	HasPiece           func(i pieceIndex) bool
	MaxRequests        int
	HasExistingRequest func(r Request) bool
	Choking            bool
	PieceAllowedFast   func(pieceIndex) bool
	DownloadRate       float64
	Age                time.Duration
	// This is passed back out at the end, so must support equality. Could be a type-param later.
	Id PeerId
}

func (p *Peer) pieceAllowedFastOrDefault(i pieceIndex) bool {
	if f := p.PieceAllowedFast; f != nil {
		return f(i)
	}
	return false
}

// TODO: This might be used in more places I think.
func (p *Peer) canRequestPiece(i pieceIndex) bool {
	return (!p.Choking || p.pieceAllowedFastOrDefault(i)) && p.HasPiece(i)
}
