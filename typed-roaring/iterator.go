package typedRoaring

import (
	"github.com/RoaringBitmap/roaring"
)

type Iterator[T BitConstraint] struct {
	roaring.IntIterator
}

func (t *Iterator[T]) Next() T {
	return T(t.IntIterator.Next())
}

func (t *Iterator[T]) AdvanceIfNeeded(minVal T) {
	t.IntIterator.AdvanceIfNeeded(uint32(minVal))
}

func (t *Iterator[T]) Initialize(a *Bitmap[T]) {
	t.IntIterator.Initialize(&a.Bitmap)
}
