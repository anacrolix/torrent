package requestStrategy

import (
	"iter"

	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type PeerRequestState struct {
	Interested bool
	Requests   PeerRequests
	// Cancelled and waiting response
	Cancelled typedRoaring.Bitmap[RequestIndex]
}

// A set of request indices iterable by order added.
type PeerRequests interface {
	// Can be more efficient than GetCardinality.
	IsEmpty() bool
	// See roaring.Bitmap.GetCardinality.
	GetCardinality() uint64
	Contains(RequestIndex) bool
	// Should not adjust iteration order if item already exists, although I don't think that usage
	// exists.
	Add(RequestIndex)
	// See roaring.Bitmap.Rank.
	Rank(RequestIndex) uint64
	// Must yield in order items were added.
	Iterate(func(RequestIndex) bool) (all bool)
	// Must yield in order items were added.
	Iterator() iter.Seq[RequestIndex]
	// See roaring.Bitmap.CheckedRemove.
	CheckedRemove(RequestIndex) bool
	// Iterate a snapshot of the values. It is safe to mutate the underlying data structure.
	IterateSnapshot(func(RequestIndex) bool)
}
