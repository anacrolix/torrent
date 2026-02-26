//go:build torrent_tidwall_btree

package indexed

import (
	"iter"

	g "github.com/anacrolix/generics"
	"github.com/tidwall/btree"
)

type tidwallBtreeSet[R any] struct {
	inner *btree.BTreeG[R]
}

func (me tidwallBtreeSet[R]) Iter(yield func(R) bool) {
	it := me.inner.Iter()
	if it.First() {
		for {
			if !yield(it.Item()) {
				return
			}
			if !it.Next() {
				break
			}
		}
	}
	it.Release()
}

func (me tidwallBtreeSet[R]) IterFrom(start R) iter.Seq[R] {
	return func(yield func(R) bool) {
		me.inner.Ascend(start, yield)
	}
}

func (me tidwallBtreeSet[R]) GetGte(start R) (ret g.Option[R]) {
	me.inner.Ascend(start, func(item R) bool {
		ret.Set(item)
		return false
	})
	return
}

func (me tidwallBtreeSet[R]) Delete(r R) (actual R, removed bool) {
	return me.inner.Delete(r)
}

func (me tidwallBtreeSet[R]) Upsert(r R) (_ R, overwrote bool) {
	return me.inner.Set(r)
}

func (me tidwallBtreeSet[R]) Contains(r R) bool {
	_, ok := me.inner.Get(r)
	return ok
}

func (me tidwallBtreeSet[R]) Len() int {
	return me.inner.Len()
}

func makeBtreeSet[R any](cmp func(R, R) int) btreeSet[R] {
	inner := btree.NewBTreeGOptions(func(a, b R) bool {
		return cmp(a, b) < 0
	}, btree.Options{
		Degree:  32,
		NoLocks: true,
	})
	return tidwallBtreeSet[R]{inner: inner}
}
