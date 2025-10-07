package indexed

import (
	"fmt"
	"iter"

	"github.com/ajwerner/btree"
)

func (me *indexImpl[R]) Iterator() Iterator[R] {
	return Iterator[R](me.index.Iterator())
}

func (me *indexImpl[R]) IterRange(gte, lt R) iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.index.Iterator()
		it.SeekGE(gte)
		for ; it.Valid() && me.cmp(it.Cur(), lt) < 0; it.Next() {
			if !yield(it.Cur()) {
				return
			}
		}
	}
}

// Not sure if this is specific to map or something else yet. But it's a pattern I keep using.
type indexImpl[R any] struct {
	// This could be made a pointer into the base eventually?
	index btree.Set[R]
	cmp   func(R, R) int
}

func (me *indexImpl[R]) Iter() iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.index.Iterator()
		for it.Next(); it.Valid(); it.Next() {
			r := it.Cur()
			if !yield(r) {
				return
			}
		}
	}
}

func (me *indexImpl[R]) Compare(l, r R) int {
	return me.cmp(l, r)
}

func (me *indexImpl[R]) Delete(r R) bool {
	return me.index.Delete(r)
}

func (me *indexImpl[R]) Add(r R) {
	_, overwrote := me.index.Upsert(r)
	if overwrote {
		panic(fmt.Sprintf("already in index: %v", r))
	}
}
