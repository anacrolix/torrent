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
	indexes       []genericRelation
	indexTriggers []triggerFunc[R]
	triggers      []triggerFunc[R]
	inited        bool
}

func (me *table[R]) GetCmp() CompareFunc[R] {
	return me.cmp
}

func (me *table[R]) SetMinRecord(min R) {
	panicif.GreaterThan(me.cmp(min, me.minRecord.Value), 0)
	me.minRecord.Set(min)
}

func (me *table[R]) Init(cmp func(a, b R) int) {
	panicif.True(me.inited)
	me.set = makeTidwallBtreeSet[R](cmp)
	me.cmp = cmp
	me.inited = true
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
	if me.minRecord.Ok {
		panicif.LessThan(me.cmp(start, me.minRecord.Value), 0)
	}
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
	remK, removed := me.set.Delete(r)
	me.Changed(g.OptionFromTuple(remK, removed), g.None[R]())
	return
}

func (me *table[R]) CreateOrReplace(r R) {
	replaced, overwrote := me.set.Upsert(r)
	me.Changed(g.OptionFromTuple(replaced, overwrote), g.Some(r))
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
	_, overwrote := me.set.Upsert(r)
	panicif.True(overwrote)
	me.Changed(g.None[R](), g.Some(r))
	return true
}

func (me *table[R]) Update(r R, updateFunc func(r R) R) (existed bool) {
	existed = me.Contains(r)
	if !existed {
		return false
	}
	newRecord := updateFunc(r)
	if newRecord == r {
		return
	}
	replaced, overwrote := me.set.Upsert(newRecord)
	panicif.False(overwrote)
	panicif.NotZero(me.cmp(r, replaced))
	me.Changed(g.Some(r), g.Some(newRecord))
	return true
}

// Should this only return a single value ever? Should we check?
func (me *table[R]) Contains(r R) bool {
	return me.set.Contains(r)
}

func (me *table[R]) Changed(old, new g.Option[R]) {
	// You should not even be trying to change a table underneath iterators. We want to know about
	// this even if nothing happens.
	me.incVersion()
	if !old.Ok && !new.Ok {
		return
	}
	if old.Ok && new.Ok {
		// I believe we have that Records are value-comparable.
		if old.Value == new.Value {
			return
		}
	}
	for _, t := range me.indexTriggers {
		t(old, new)
	}
	me.runTriggers(old, new)
}

// When you know the existing state and the destination state. Most efficient.
func (me *table[R]) Change(old, new g.Option[R]) {
	if old.Ok {
		if new.Ok {
			// We can't guard deletion in case the compare function is partial, because we may be
			// updating unordered fields.
			_, deleted := me.set.Delete(old.Value)
			panicif.False(deleted)
			_, overwrote := me.set.Upsert(new.Value)
			// What about deleting only if the upsert doesn't clobber here?
			panicif.True(overwrote)
		} else {
			_, deleted := me.set.Delete(old.Value)
			panicif.False(deleted)
		}
	} else {
		if new.Ok {
			_, overwrote := me.set.Upsert(new.Value)
			panicif.True(overwrote)
		} else {
			return
		}
	}
	me.Changed(old, new)
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
	// Don't need version checking since we don't iterate.
	for ret.Value = range me.IterFrom(r) {
		ret.Ok = true
		break
	}
	return
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
