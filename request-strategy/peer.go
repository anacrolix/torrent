package requestStrategy

import (
	"sync/atomic"

	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type PeerRequestState struct {
	Interested bool
	Requests   PeerRequests
	// Cancelled and waiting response
	Cancelled typedRoaring.Bitmap[RequestIndex]
	CancelCounter atomic.Int32
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
	Iterate(func(RequestIndex) bool)
	// See roaring.Bitmap.CheckedRemove.
	CheckedRemove(RequestIndex) bool
	// Iterate a snapshot of the values. It is safe to mutate the underlying data structure.
	IterateSnapshot(func(RequestIndex) bool)

	Bitmap() *typedRoaring.Bitmap[RequestIndex]
}

/*type SyncBitmap struct {
    mu     sync.Mutex
    bitmap typedRoaring.Bitmap[RequestIndex]
}

func (sb *SyncBitmap) GetCardinality() uint64 {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.GetCardinality()
}

func (sb *SyncBitmap) Contains(r RequestIndex) bool {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.Contains(r)
}

func (sb *SyncBitmap) Remove(r RequestIndex) {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    sb.bitmap.Remove(r)
}

func (sb *SyncBitmap) CheckedAdd(r RequestIndex) bool{
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.CheckedAdd(r)
}

func (sb *SyncBitmap) CheckedRemove(r RequestIndex) bool{
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.CheckedRemove(r)
}

func (sb *SyncBitmap) Clone() typedRoaring.Bitmap[RequestIndex] {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.Clone()
}

func (sb *SyncBitmap) IsEmpty() bool {
    sb.mu.Lock()
    defer sb.mu.Unlock()
    return sb.bitmap.IsEmpty()
}*/