package indexed

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/option"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type mapTriggerFunc[K, V any] func(key K, old, new g.Option[V])

// A table where the record has a key and value. Currently maps onto Table2 using Pair, but perhaps
// requiring the record implements a KeyValue interface would be better. K and V are Record to
// propagate the comparable requirement for now. TODO: We could use an actual map, and implement
// relation. Another thing we can do is actually use btree.Map's value, I'm not sure if btree is
// smarter about that.
type Map[K, V Record] struct {
	Table2[K, V]
	keyCmp func(a, b K) int
}

func (me *Map[K, V]) Init(cmp func(a, b K) int) {
	me.Table2.Init(func(a, b Pair[K, V]) int {
		return cmp(a.Left, b.Left)
	})
	me.keyCmp = cmp
}

type UpdateResult struct {
	Exists  bool
	Changed bool
}

// Inner table update is unimplemented, because
func (me *Map[K, V]) Update(key K, updateFunc func(V) V) (res UpdateResult) {
	return me.Table2.Update(
		Pair[K, V]{Left: key},
		func(r Pair[K, V]) Pair[K, V] {
			r.Right = updateFunc(r.Right)
			return r
		},
	)
}

func (me *Map[K, V]) Alter(key K, updateFunc func(V, bool) (V, bool)) {
	oldV, oldOk := me.Get(key)
	newV, newOk := updateFunc(oldV, oldOk)
	me.Change(
		g.OptionFromTuple(NewPair(key, oldV), oldOk),
		g.OptionFromTuple(NewPair(key, newV), newOk))
}

func (me *Map[K, V]) Get(k K) (v V, ok bool) {
	return option.Map(pairMapRight, GetEq(me, Pair[K, V]{Left: k})).AsTuple()
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

// Maps don't compare on the value, so we can leave them as zeroes.
func (me *Map[K, V]) SetMinRecord(min K) {
	me.table.SetMinRecord(Pair[K, V]{Left: min})
}
