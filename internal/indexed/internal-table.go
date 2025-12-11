package indexed

import (
	"fmt"
	"iter"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/amortize"
)

type table[R Record] struct {
	minRecord g.Option[R]
	set       btreeSet[R]
	// Tracks changes to the btree
	version       int
	cmp           CompareFunc[R]
	insteadOf     []InsteadOf[R]
	indexes       []genericRelation
	indexTriggers []triggerFunc[R]
	// I want to believe this isn't useful for indexes but I could be wrong.
	triggers []triggerFunc[R]
	inited   bool
	// The table must not be modified. We're probably handling triggers on it.
	poisoned bool
}

func (me *table[R]) GetCmp() CompareFunc[R] {
	return me.cmp
}

func (me *table[R]) SetMinRecord(min R) {
	panicif.GreaterThan(me.cmp(min, me.minRecord.Value), 0)
	me.minRecord.Set(min)
}

func (me *table[R]) Init(cmp CompareFunc[R]) {
	panicif.True(me.inited)
	me.inited = true
	me.set = makeAjwernerSet(cmp)
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

func (me *table[R]) incVersion() {
	me.version++
}

func (me *table[R]) assertIteratorVersion(it btreeIterator[R]) {
	panicif.NotEq(me.version, it.version)
}

func (me *table[R]) IterFrom(start R) iter.Seq[R] {
	return me.set.IterFrom(start)
}

func (me *table[R]) IterFromWhile(gte R, while func(R) bool) iter.Seq[R] {
	return func(yield func(R) bool) {
		for r := range me.IterFrom(gte) {
			if !while(r) || !yield(r) {
				return
			}
		}
	}
}

func (me *table[R]) SelectFirstIf(gte R, filter func(r R) bool) (ret g.Option[R]) {
	for r := range me.IterFromWhile(gte, filter) {
		ret.Set(r)
		break
	}
	return
}

func (me *table[R]) checkWhereGotFirst(first g.Option[R], where func(r R) bool) {
	if !amortize.Try() {
		return
	}
	var slowRet g.Option[R]
	for r := range me.Iter {
		if where(r) {
			slowRet.Set(r)
			break
		}
	}
	if first.Ok != slowRet.Ok || first.Ok && me.cmp(first.Value, slowRet.Value) != 0 {
		fmt.Printf("%#v\n", first.Value)
		fmt.Printf("%#v\n", slowRet.Value)
		panic("herp")
	}
}

func (me *table[R]) SelectFirstWhere(gte R, where func(r R) bool) (ret g.Option[R]) {
	for r := range me.IterFromWhile(gte, where) {
		ret.Set(r)
		break
	}
	checkWhereGotFirst(me, ret, where)
	return
}

func (me *table[R]) Delete(r R) (removed bool) {
	return me.Change(g.Some(r), g.None[R]())
}

func (me *table[R]) CreateOrReplace(r R) {
	me.Change(GetEq(me, r), g.Some(r))
}

func (me *table[R]) Iter(yield func(R) bool) {
	for r := range me.set.Iter {
		if me.minRecord.Ok && amortize.Try() {
			panicif.LessThan(me.cmp(r, me.minRecord.Value), 0)
		}
		if !yield(r) {
			return
		}
	}
}

func (me *table[R]) Create(r R) (created bool) {
	if me.set.Contains(r) {
		return false
	}
	return me.Change(g.None[R](), g.Some(r))
}

// Doesn't currently allow replacing items other than itself.
func (me *table[R]) Update(start R, updateFunc func(r R) R) (res UpdateResult) {
	gte := me.GetGte(start)
	if !gte.Ok {
		return
	}
	old := gte.Value
	if me.cmp(old, start) != 0 {
		return
	}
	res.Exists = true
	new := updateFunc(old)
	res.Changed = me.Change(g.Some(old), g.Some(new))
	return
}

// Should this only return a single value ever? Should we check?
func (me *table[R]) Contains(r R) bool {
	return me.set.Contains(r)
}

func (me *table[R]) Changed(old, new g.Option[R]) {
	panicif.Eq(old, new)
	for _, t := range me.indexTriggers {
		t(old, new)
	}
	me.runTriggers(old, new)
}

// When you know the existing state and the destination state. Most efficient. This is used by
// indexes, but allows insteadOf. Not sure if that's a good idea.
func (me *table[R]) Change(old, new g.Option[R]) bool {
	// You should not even be trying to change a table underneath iterators. We want to know about
	// this even if nothing happens.
	me.incVersion()
	// Make sure nothing invalidates the old/new values we're handling.
	me.Poison()
	defer me.Unpoison()
	new = me.applyInsteadOf(old, new)
	if old.Ok {
		if new.Ok {
			if old.Value == new.Value {
				return false
			}
			replaced, overwrote := me.set.Upsert(new.Value)
			// What about deleting only if the upsert doesn't clobber here?
			if overwrote {
				panicif.NotEq(replaced, old.Value)
			} else {
				actual, removed := me.set.Delete(old.Value)
				panicif.False(removed)
				panicif.NotEq(old.Value, actual)
			}
		} else {
			actual, deleted := me.set.Delete(old.Value)
			if !deleted {
				return false
			}
			// Actually we should allow this since there's no shortcut to prevent unnecessary
			// changes, and we avoid a lookup to get the full value.
			panicif.NotZero(me.cmp(old.Value, actual))
			old.Value = actual
		}
	} else {
		if new.Ok {
			_, overwrote := me.set.Upsert(new.Value)
			panicif.True(overwrote)
		} else {
			return false
		}
	}
	me.Changed(old, new)
	return true
}

func (me *table[R]) GetFirst() (r R, ok bool) {
	for r = range me.Iter {
		ok = true
		break
	}
	if amortize.Try() {
		panicif.NotEq(g.OptionFromTuple(r, ok), me.GetGte(me.MinRecord()))
	}
	return
}

// Gets the first record greater than or equal. Hope to avoid allocation for iterator.
func (me *table[R]) GetGte(r R) (ret g.Option[R]) {
	return me.set.GetGte(r)
}

// Not count because that could imply more than O(1) work.
func (me *table[R]) Len() int {
	return me.set.Len()
}

// Returns the minimal record of type R, which may not be the same as the zero value for the record.
// Convenient to avoid having to look up complex types for small expressions. Could be a global
// function. Should definitely be if this is invoked through an interface. Panics if the min record
// wasn't set. The concept of MinRecord might be flawed if there are conditions in the ordering of
// values in a record. In that case the user may have to modify "intermediate" fields in order to
// set a GTE record that's appropriate midway in the table.
func (me *table[R]) MinRecord() (_ R) {
	return me.minRecord.Unwrap()
}

// Detect recursive use of table when it's a bad idea. To be replaced with a better fix sometime.
func (me *table[R]) Poison() {
	panicif.True(me.poisoned)
	me.poisoned = true
}

func (me *table[R]) Unpoison() {
	panicif.False(me.poisoned)
	me.poisoned = false
}

func (me *table[R]) applyInsteadOf(old, new g.Option[R]) g.Option[R] {
	for _, f := range me.insteadOf {
		new = f(old, new)
	}
	return new
}
