package indexed

import (
	"iter"

	"github.com/ajwerner/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type table[R any] struct {
	set      btree.Set[R]
	cmp      CompareFunc[R]
	triggers []triggerFunc[R]
}

func (me *table[R]) Init(cmp func(a, b R) int) {
	me.set = btree.MakeSet[R](cmp)
	me.cmp = cmp
}

func (me *table[R]) runTriggers(old, new g.Option[R]) {
	for _, t := range me.triggers {
		t(old, new)
	}
}

func (me *table[R]) OnChange(t triggerFunc[R]) {
	me.triggers = append(me.triggers, t)
}

func (me *table[R]) IterFrom(start R) iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.set.Iterator()
		it.SeekGE(start)
		for ; it.Valid(); it.Next() {
			if !yield(it.Cur()) {
				return
			}
		}
	}
}

func (me *table[R]) IterRange(gte, lt R) iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.set.Iterator()
		it.SeekGE(gte)
		for ; it.Valid() && it.Compare(it.Cur(), lt) < 0; it.Next() {
			if !yield(it.Cur()) {
				return
			}
		}
	}
}

func (me *table[R]) Delete(r R) (removed bool) {
	removed = me.set.Delete(r)
	me.runTriggers(g.OptionFromTuple(r, removed), g.None[R]())
	return
}

func (me *table[R]) CreateOrReplace(r R) {
	_, overwrote := me.set.Upsert(r)
	panicif.True(overwrote)
}

func (me *table[R]) Iter() iter.Seq[R] {
	return func(yield func(R) bool) {
		it := me.set.Iterator()
		for it.First(); it.Valid(); it.Next() {
			if !yield(it.Cur()) {
				return
			}
		}
	}
}

func (me *table[R]) Create(r R) (created bool) {
	_, exists := me.set.Get(r)
	if exists {
		return false
	}
	_, overwrote := me.set.Upsert(r)
	panicif.True(overwrote)
	me.Changed(g.None[R](), g.Some(r))
	return true
}

func (me *table[R]) Update(r R, updateFunc func(r R) R) (existed bool) {
	oldRecord, existed := me.Get(r)
	if !existed {
		return false
	}
	newRecord := updateFunc(oldRecord)
	replaced, overwrote := me.set.Upsert(newRecord)
	panicif.False(overwrote)
	panicif.NotZero(me.cmp(oldRecord, replaced))
	me.Changed(g.Some(oldRecord), g.Some(newRecord))
	return true
}

func (me *table[R]) Get(r R) (actual R, ok bool) {
	it := me.set.Iterator()
	it.SeekGE(r)
	if !it.Valid() {
		return
	}
	if it.Compare(it.Cur(), r) != 0 {
		return
	}
	return it.Cur(), true
}

func (me *table[R]) CreateOrUpdate(r R, updateFunc func(old R) R) (created bool) {
	old, existed := me.Get(r)
	new := updateFunc(old)
	panicif.NotEq(me.set.Delete(r), existed)
	_, overwrote := me.set.Upsert(new)
	// Assume we don't want to replace after update...? What is that in SQL speak?
	panicif.True(overwrote)
	created = !existed
	me.runTriggers(g.OptionFromTuple(old, existed), g.Some(new))
	return
}

func (me *table[R]) Changed(old, new g.Option[R]) {
	me.runTriggers(old, new)
}

func (me *table[R]) Change(old, new g.Option[R]) {
	if old.Ok {
		if new.Ok {
			if me.cmp(old.Value, new.Value) == 0 {
				return
			}
			// Could optimize to see if things *move* here by checking predecessor and successor?
			panicif.False(me.set.Delete(old.Value))
			_, overwrote := me.set.Upsert(new.Value)
			panicif.True(overwrote)
		} else {
			panicif.False(me.set.Delete(old.Value))
		}
	} else {
		if new.Ok {
			_, overwrote := me.set.Upsert(new.Value)
			panicif.True(overwrote)
		} else {
			panic("both old and new are none")
		}
	}
}
