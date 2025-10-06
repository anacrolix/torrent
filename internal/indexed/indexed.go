package indexed

import (
	"fmt"
	"iter"

	"github.com/ajwerner/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

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

// Minimizes duplication of primary key by only storing it in the primary map. R could be row or Record, I see no difference.
type Map[K PrimaryKey[K], R Record[K]] struct {
	ops    TableOps[K, R]
	m      map[K]R
	single *indexImpl[R]
}

// Single index map
func NewMap[K PrimaryKey[K], R Record[K]](
	ops TableOps[K, R],
	cmp func(R, R) int,
) *Map[K, R] {
	panicif.Nil(ops.PrimaryKey)
	panicif.Nil(ops.ComparePrimaryKey)
	fullCmp := ops.indexFullCompare(cmp)
	ret := &Map[K, R]{
		ops: ops,
		m:   make(map[K]R),
		single: &indexImpl[R]{
			index: btree.MakeSet[R](fullCmp),
			cmp:   fullCmp,
		},
	}
	return ret
}

// Creates new record, returns true if it was created, false if a record with the same primary key
// already exists.
func (me *Map[K, R]) Create(record R) bool {
	pk := *me.ops.PrimaryKey(&record)
	if g.MapContains(me.m, pk) {
		return false
	}
	me.onModified(&record)
	me.m[pk] = record
	me.single.Add(record)
	return true
}

// Creates or replaces record, returning the old record if it existed.
func (me *Map[K, R]) Upsert(record R) g.Option[R] {
	pk := *me.primaryKey(&record)
	me.onModified(&record)
	old, ok := me.m[pk]
	me.m[pk] = record
	if ok {
		if me.single.Compare(record, old) != 0 {
			me.single.Delete(old)
			me.single.Add(record)
		}
	} else {
		me.single.Add(record)
	}
	return g.OptionFromTuple(old, ok)
}

// Creates or updates record.
func (me *Map[K, R]) CreateOrUpdate(pk K, updateFunc func(existed bool, r R) R) {
	oldRecord, existed := me.m[pk]
	if existed {
		me.single.Delete(oldRecord)
		panicif.NotEq(*me.primaryKey(&oldRecord), pk)
	} else {
		*me.primaryKey(&oldRecord) = pk
	}
	newRecord := updateFunc(existed, oldRecord)
	me.onModified(&newRecord)
	panicif.NotEq(*me.primaryKey(&newRecord), pk)
	me.m[pk] = newRecord
	me.single.Add(newRecord)
}

func (me *Map[K, R]) Update(pk K, updateFunc func(record R) R) (exists bool) {
	old, exists := me.m[pk]
	if !exists {
		return
	}
	me.single.Delete(old)
	new := updateFunc(old)
	me.onModified(&new)
	me.m[pk] = new
	me.single.Add(new)
	return
}

func (me *Map[K, R]) Iter() iter.Seq[R] {
	return me.single.Iter()
}

func (me *Map[K, R]) IterPrimaryKeys() iter.Seq[K] {
	return func(yield func(K) bool) {
		for r := range me.Iter() {
			if !yield(*me.primaryKey(&r)) {
				return
			}
		}
	}
}

func (me *Map[K, R]) Get(pk K) g.Option[R] {
	v, ok := me.m[pk]
	return g.OptionFromTuple(v, ok)
}

type Iterator[K, V any] = btree.SetIterator[V]

func (me *Map[K, R]) Iterator() Iterator[K, R] {
	return Iterator[K, R](me.single.index.Iterator())
}

func (me *Map[K, R]) onModified(r *R) {
	if me.ops.OnModified != nil {
		*r = me.ops.OnModified(*r)
	}
}

func (me *Map[K, R]) primaryKey(r *R) *K {
	return me.ops.PrimaryKey(r)
}
