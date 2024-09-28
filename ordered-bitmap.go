package torrent

import (
	"iter"

	g "github.com/anacrolix/generics"
	list "github.com/bahlo/generic-list-go"

	"github.com/anacrolix/torrent/typed-roaring"
)

type orderedBitmap[T typedRoaring.BitConstraint] struct {
	bitmap typedRoaring.Bitmap[T]
	// There should be way more efficient ways to do this.
	order    list.List[T]
	elements map[T]*list.Element[T]
}

func (o *orderedBitmap[T]) IterateSnapshot(f func(T) bool) {
	o.bitmap.Clone().Iterate(f)
}

func (o *orderedBitmap[T]) IsEmpty() bool {
	return o.bitmap.IsEmpty()
}

func (o *orderedBitmap[T]) GetCardinality() uint64 {
	return uint64(o.order.Len())
}

func (o *orderedBitmap[T]) Contains(index T) bool {
	return o.bitmap.Contains(index)
}

func (o *orderedBitmap[T]) Add(index T) {
	o.bitmap.Add(index)
	if _, ok := o.elements[index]; !ok {
		g.MakeMapIfNilAndSet(&o.elements, index, o.order.PushBack(index))
	}
}

func (o *orderedBitmap[T]) Rank(index T) uint64 {
	return o.bitmap.Rank(index)
}

func (o *orderedBitmap[T]) Iterate(f func(T) bool) (all bool) {
	for e := o.order.Front(); e != nil; e = e.Next() {
		if !f(e.Value) {
			return
		}
	}
	all = true
	return
}

func (o *orderedBitmap[T]) Iterator() iter.Seq[T] {
	return func(yield func(T) bool) {
		for e := o.order.Front(); e != nil; e = e.Next() {
			if !yield(e.Value) {
				return
			}
		}
	}
}

func (o *orderedBitmap[T]) CheckedRemove(index T) bool {
	if !o.bitmap.CheckedRemove(index) {
		return false
	}
	o.order.Remove(o.elements[index])
	delete(o.elements, index)
	return true
}
