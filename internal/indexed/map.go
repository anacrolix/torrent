package indexed

import (
	"iter"
	"maps"
	"slices"

	"github.com/ajwerner/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

// Minimizes duplication of primary key by only storing it in the primary map. R could be row or Record, I see no difference.
type Map[K PrimaryKey[K], R Record[K]] struct {
	ops     TableOps[K, R]
	m       map[K]R
	indices []*indexImpl[R]
}

func (me *Map[K, R]) AddIndex(cmp RecordCmpFunc[R]) Index[R] {
	fullCmp := me.ops.indexFullCompare(cmp)
	newIndex := &indexImpl[R]{
		index: btree.MakeSet[R](fullCmp),
		cmp:   fullCmp,
	}
	for _, r := range me.m {
		newIndex.Add(r)
	}
	me.indices = append(me.indices, newIndex)
	return newIndex
}

// Single index map
func NewMap[K PrimaryKey[K], R Record[K]](
	ops TableOps[K, R],
) *Map[K, R] {
	panicif.Nil(ops.PrimaryKey)
	panicif.Nil(ops.ComparePrimaryKey)
	ret := &Map[K, R]{
		ops: ops,
		m:   make(map[K]R),
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
	me.addToIndices(record)
	return true
}

func (me *Map[K, R]) addToIndices(r R) {
	for index := range me.iterIndices() {
		index.Add(r)
	}
}

func (me *Map[K, R]) deleteFromIndices(r R) {
	for index := range me.iterIndices() {
		index.Delete(r)
	}
}

func (me *Map[K, R]) iterIndices() iter.Seq[*indexImpl[R]] {
	return slices.Values(me.indices)
}

// Creates or replaces record, returning the old record if it existed.
func (me *Map[K, R]) Upsert(record R) g.Option[R] {
	pk := *me.primaryKey(&record)
	me.onModified(&record)
	old, ok := me.m[pk]
	me.m[pk] = record
	me.deleteFromIndices(record)
	me.addToIndices(record)
	return g.OptionFromTuple(old, ok)
}

// Creates or updates record.
func (me *Map[K, R]) CreateOrUpdate(pk K, updateFunc func(existed bool, r R) R) {
	oldRecord, existed := me.m[pk]
	if existed {
		me.deleteFromIndices(oldRecord)
		panicif.NotEq(*me.primaryKey(&oldRecord), pk)
	} else {
		*me.primaryKey(&oldRecord) = pk
	}
	newRecord := updateFunc(existed, oldRecord)
	me.onModified(&newRecord)
	panicif.NotEq(*me.primaryKey(&newRecord), pk)
	me.m[pk] = newRecord
	me.addToIndices(newRecord)
}

func (me *Map[K, R]) Update(pk K, updateFunc func(record R) R) (exists bool) {
	old, exists := me.m[pk]
	if !exists {
		return
	}
	me.deleteFromIndices(old)
	new := updateFunc(old)
	me.onModified(&new)
	me.m[pk] = new
	me.addToIndices(new)
	return
}

func (me *Map[K, R]) Iter() iter.Seq[R] {
	return maps.Values(me.m)
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

func (me *Map[K, R]) onModified(r *R) {
	if me.ops.OnModified != nil {
		*r = me.ops.OnModified(*r)
	}
}

func (me *Map[K, R]) primaryKey(r *R) *K {
	return me.ops.PrimaryKey(r)
}

func (me *Map[K, R]) IterIndexPrimaryKeys(index Index[R]) iter.Seq[K] {
	return func(yield func(K) bool) {
		for r := range index.Iter() {
			if !yield(*me.primaryKey(&r)) {
				return
			}
		}
	}
}
