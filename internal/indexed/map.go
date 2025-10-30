package indexed

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/option"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type mapTriggerFunc[K, V any] func(key K, old, new g.Option[V])

// A table where the record has a key and value. Currently maps onto Table2 using Pair, but perhaps
// requiring the record implements a KeyValue interface would be better.
type Map[K, V any] struct {
	Table2[K, V]
	keyCmp func(a, b K) int
}

func (me *Map[K, V]) Init(cmp func(a, b K) int) {
	me.Table2.Init(func(a, b Pair[K, V]) int {
		return cmp(a.Left, b.Left)
	})
	me.keyCmp = cmp
}

func (me *Map[K, V]) Update(key K, updateFunc func(V) V) (exists bool) {
	start := Pair[K, V]{Left: key}
	var first g.Option[Pair[K, V]]
	for r := range me.IterFromWhile(start, func(p Pair[K, V]) bool {
		return me.keyCmp(p.Left, key) == 0
	}) {
		panicif.True(first.Ok)
		first.Set(r)
	}
	if !first.Ok {
		return
	}
	// Must finish iterating before doing update.
	panicif.False(me.Table2.Update(
		first.Value,
		func(r Pair[K, V]) Pair[K, V] {
			r.Right = updateFunc(r.Right)
			return r
		},
	))
	return true
}

func (me *Map[K, V]) Alter(key K, updateFunc func(V, bool) (V, bool)) {
	oldV, oldOk := me.Get(key)
	newV, newOk := updateFunc(oldV, oldOk)
	me.Change(
		g.OptionFromTuple(NewPair(key, oldV), oldOk),
		g.OptionFromTuple(NewPair(key, newV), newOk))
}

func (me *Map[K, V]) Get(k K) (v V, ok bool) {
	it := me.set.Iterator()
	it.SeekGE(Pair[K, V]{Left: k})
	if it.Valid() && me.keyCmp(it.Cur().Left, k) == 0 {
		v = it.Cur().Right
		ok = true
	}
	return
}

func (me *Map[K, V]) ContainsKey(k K) bool {
	_, ok := me.Get(k)
	return ok
}

func (me *Map[K, V]) Delete(k K) (removed bool) {
	return me.Table2.Delete(Pair[K, V]{Left: k})
}

// Update the function otherwise create it, in both cases using the update function provided.
func (me *Map[K, V]) UpdateOrCreate(k K, updateFunc func(old V) V) (created bool) {
	me.Alter(k, func(v V, existed bool) (V, bool) {
		created = !existed
		return updateFunc(v), true
	})
	return
}

func (me *Map[K, V]) OnValueChange(do mapTriggerFunc[K, V]) {
	me.OnChange(func(old, new g.Option[Pair[K, V]]) {
		if old.Ok && new.Ok {
			panicif.NotZero(me.keyCmp(old.Value.Left, new.Value.Left))
		}
		// Key must be one or the other.
		var key K
		if old.Ok {
			key = old.Value.Left
		} else {
			key = new.Unwrap().Left
		}
		do(key, option.Map(pairMapRight, old), option.Map(pairMapRight, new))
	})
}
