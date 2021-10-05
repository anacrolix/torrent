package request_strategy

import (
	"time"

	"github.com/RoaringBitmap/roaring"
)

type PeerNextRequestState struct {
	Interested bool
	Requests   roaring.Bitmap
}

type PeerId interface {
	Uintptr() uintptr
}

type Peer struct {
	Pieces           roaring.Bitmap
	MaxRequests      int
	ExistingRequests roaring.Bitmap
	Choking          bool
	PieceAllowedFast roaring.Bitmap
	DownloadRate     float64
	Age              time.Duration
	// This is passed back out at the end, so must support equality. Could be a type-param later.
	Id PeerId
}

// TODO: This might be used in more places I think.
func (p *Peer) canRequestPiece(i pieceIndex) bool {
	return (!p.Choking || p.PieceAllowedFast.Contains(uint32(i))) && p.HasPiece(i)
}

func (p *Peer) HasPiece(i pieceIndex) bool {
	return p.Pieces.Contains(uint32(i))
}
