package indexed

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type Map[K, V any] struct {
	Table2[K, V]
	keyCmp func(a, b K) int
}

func (me *Map[K, V]) Init(cmp func(a, b K) int) {
	me.Table2.Init(func(a, b MapRecord[K, V]) int {
		return cmp(a.Key, b.Key)
	})
	me.keyCmp = cmp
}

func (me *Map[K, V]) Update(key K, updateFunc func(V) V) (exists bool) {
	start := MapRecord[K, V]{Key: key}
	it := me.set.Iterator()
	it.SeekGE(start)
	updated := 0
	for ; it.Valid() && me.keyCmp(it.Cur().Key, key) == 0; it.Next() {
		panicif.False(me.Table2.Update(it.Cur(), func(r MapRecord[K, V]) MapRecord[K, V] {
			r.Value = updateFunc(r.Value)
			return r
		}))
		updated++
	}
	panicif.GreaterThan(updated, 1)
	return updated > 0
}

func (me *Map[K, V]) Get(k K) (V, bool) {
	r, ok := me.Table2.Get(MapRecord[K, V]{Key: k})
	return r.Value, ok
}

func (me *Map[K, V]) Delete(k K) (removed bool) {
	return me.Table2.Delete(MapRecord[K, V]{Key: k})
}

func (me *Map[K, V]) CreateOrUpdate(k K, updateFunc func(old V) V) (created bool) {
	oldValue, existed := me.Get(k)
	newValue := updateFunc(oldValue)
	panicif.NotEq(me.Delete(k), existed)
	newRecord := MapRecord[K, V]{k, newValue}
	_, overwrote := me.set.Upsert(newRecord)
	// Assume we don't want to replace after update...? What is that in SQL speak?
	panicif.True(overwrote)
	created = !existed
	me.runTriggers(g.OptionFromTuple(MapRecord[K, V]{Key: k, Value: oldValue}, existed), g.Some(newRecord))
	return
}
