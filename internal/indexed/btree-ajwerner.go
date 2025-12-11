package indexed

import (
	"iter"

	"github.com/anacrolix/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type btreeIterator[R any] struct {
	btree.MapIterator[R, struct{}]
	version int
}

type ajwernerBtreeSet[R any] struct {
	inner   btree.Set[R]
	version int
}

func (me *ajwernerBtreeSet[R]) Delete(r R) (actual R, removed bool) {
	actual, _, removed = me.inner.Map.Delete(r)
	return
}

func (me *ajwernerBtreeSet[R]) Upsert(r R) (_ R, overwrote bool) {
	return me.inner.Upsert(r)
}

func (me *ajwernerBtreeSet[R]) Contains(r R) bool {
	_, ok := me.inner.Get(r)
	return ok
}

func (me *ajwernerBtreeSet[R]) Len() int {
	return me.inner.Len()
}

func (me *ajwernerBtreeSet[R]) getBtreeIterator() btreeIterator[R] {
	return btreeIterator[R]{
		MapIterator: me.inner.Iterator(),
		version:     me.version,
	}
}

func (me *ajwernerBtreeSet[R]) assertIteratorVersion(it btreeIterator[R]) {
	panicif.NotEq(me.version, it.version)
}

func (me *ajwernerBtreeSet[R]) Iter(yield func(R) bool) {
	it := me.getBtreeIterator()
	for it.First(); it.Valid(); it.Next() {
		if !yield(it.Cur()) {
			return
		}
	}
}

func (me *ajwernerBtreeSet[R]) IterFrom(start R) iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.getBtreeIterator()
		it.SeekGE(start)
		for ; it.Valid(); it.Next() {
			if !yield(it.Cur()) {
				return
			}
			me.assertIteratorVersion(it)
		}
	}
}

func (me *ajwernerBtreeSet[R]) GetGte(start R) (_ g.Option[R]) {
	it := me.getBtreeIterator()
	it.SeekGE(start)
	if !it.Valid() {
		return
	}
	return g.Some(it.Cur())
}

func makeAjwernerSet[R any](cmp func(R, R) int) *ajwernerBtreeSet[R] {
	return &ajwernerBtreeSet[R]{inner: btree.MakeSet(cmp)}
}
