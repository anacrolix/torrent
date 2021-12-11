package request_strategy

import (
	"github.com/RoaringBitmap/roaring"
)

type PeerNextRequestState struct {
	Interested bool
	Requests   roaring.Bitmap
}
