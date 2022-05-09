package typedRoaring

import (
	"github.com/RoaringBitmap/roaring"
)

type Iterator[T BitConstraint] struct {
	roaring.IntPeekable
}

func (t Iterator[T]) Next() T {
	return T(t.IntPeekable.Next())
}

func (t Iterator[T]) AdvanceIfNeeded(minVal T) {
	t.IntPeekable.AdvanceIfNeeded(uint32(minVal))
}
