package torrent

import (
	"sync"

	"github.com/RoaringBitmap/roaring/v2"
)

func newBitQueue() *bitQueue {
	return &bitQueue{
		mu: &sync.RWMutex{},
		RB: roaring.NewBitmap(),
	}
}

type bitQueue struct {
	mu *sync.RWMutex
	RB *roaring.Bitmap
}

func (t bitQueue) Count() (i int) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return int(t.RB.GetCardinality())
}

func (t bitQueue) Pop() (i int, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.RB.IsEmpty() {
		return 0, false
	}

	tmp := t.RB.Minimum()

	t.RB.Remove(tmp)
	return int(tmp), true
}

func (t bitQueue) Push(i int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.RB.AddInt(i)
}

func (t bitQueue) PushBitmap(o *roaring.Bitmap) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.RB.Or(o)
}
