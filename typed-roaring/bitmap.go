package typedRoaring

import (
	"github.com/RoaringBitmap/roaring"
)

type Bitmap[T BitConstraint] struct {
	roaring.Bitmap
}

func (me *Bitmap[T]) Contains(x T) bool {
	return me.Bitmap.Contains(uint32(x))
}

func (me Bitmap[T]) Iterate(f func(x T) bool) {
	me.Bitmap.Iterate(func(x uint32) bool {
		return f(T(x))
	})
}

func (me *Bitmap[T]) Add(x T) {
	me.Bitmap.Add(uint32(x))
}

func (me *Bitmap[T]) Rank(x T) uint64 {
	return me.Bitmap.Rank(uint32(x))
}

func (me *Bitmap[T]) CheckedRemove(x T) bool {
	return me.Bitmap.CheckedRemove(uint32(x))
}

func (me *Bitmap[T]) Clone() Bitmap[T] {
	return Bitmap[T]{*me.Bitmap.Clone()}
}

func (me *Bitmap[T]) CheckedAdd(x T) bool {
	return me.Bitmap.CheckedAdd(uint32(x))
}

func (me *Bitmap[T]) Remove(x T) {
	me.Bitmap.Remove(uint32(x))
}

// Returns an uninitialized iterator for the type of the receiver.
func (Bitmap[T]) IteratorType() Iterator[T] {
	return Iterator[T]{}
}

// TODO: Override Bitmap.Iterator.
