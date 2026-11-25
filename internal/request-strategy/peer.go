package requestStrategy

import (
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type PeerRequestState struct {
	Interested bool
	Requests   *PeerRequests
	// Cancelled and waiting response
	Cancelled typedRoaring.Bitmap[RequestIndex]
}

// A set of request indices iterable by order added.
type PeerRequests = orderedBitmap[RequestIndex]
