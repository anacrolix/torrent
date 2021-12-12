package request_strategy

import (
	"github.com/RoaringBitmap/roaring"
)

type PeerRequestState struct {
	Interested bool
	// Expecting
	Requests roaring.Bitmap
	// Cancelled and waiting response
	Cancelled roaring.Bitmap
}
