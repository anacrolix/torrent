package indexed

import (
	"cmp"
	"iter"

	"github.com/ajwerner/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

//
//type PrimaryKey[T any] interface {
//	comparable
//	Compare(other T) int
//}

type PrimaryKey[T any] interface {
	comparable
	// Or do we ask the user to provide a comparator, expecting that cmp.Compare works for builtins?
	Compare(other T) int
}

type RecordCmpFunc[K, V any] func(a, b Record[K, V]) int

// I think we should just inline this and require PrimaryKey to be implemented to extract K.
type Record[K, V any] struct {
	PrimaryKey K
	Values     V
}

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
	panicif.True(overwrote)
}

// Minimizes duplication of primary key by only storing it in the primary map.
type Map[K comparable, V any] struct {
	m      map[K]V
	single *indexImpl[Record[K, V]]
}

type index[R any] interface {
	Delete(r R) bool
	Add(r R)
	Compare(R, R) int
	Iter() iter.Seq[R]
}

func orComparePrimaryKey[K PrimaryKey[K], V any](first RecordCmpFunc[K, V]) RecordCmpFunc[K, V] {
	return func(a, b Record[K, V]) int {
		return cmp.Or(first(a, b), a.PrimaryKey.Compare(b.PrimaryKey))
	}
}

// Single index map
func NewMap[K PrimaryKey[K], V any](
	cmp func(Record[K, V], Record[K, V]) int,
) *Map[K, V] {
	fullCmp := orComparePrimaryKey(cmp)
	ret := &Map[K, V]{
		m: make(map[K]V),
		single: &indexImpl[Record[K, V]]{
			index: btree.MakeSet[Record[K, V]](fullCmp),
			cmp:   fullCmp,
		},
	}
	return ret
}

func (me *Map[K, V]) Upsert(pk K, values V) g.Option[V] {
	newRecord := Record[K, V]{pk, values}
	oldValues, ok := me.m[pk]
	me.m[pk] = values
	if ok {
		oldRecord := Record[K, V]{pk, oldValues}
		if me.single.Compare(newRecord, oldRecord) != 0 {
			me.single.Delete(oldRecord)
			me.single.Add(newRecord)
		}
	} else {
		me.single.Add(newRecord)
	}
	return g.OptionFromTuple(oldValues, ok)
}

func (me *Map[K, V]) CreateOrUpdate(pk K, updateFunc func(existed bool, values *V)) {
	values, existed := me.m[pk]
	if existed {
		me.single.Delete(Record[K, V]{pk, values})
	}
	updateFunc(existed, &values)
	newRecord := Record[K, V]{pk, values}
	me.m[pk] = values
	me.single.Add(newRecord)
}

func (me *Map[K, V]) Update(pk K, updateFunc func(values *V)) (exists bool) {
	values, exists := me.m[pk]
	if exists {
		me.single.Delete(Record[K, V]{pk, values})
		updateFunc(&values)
	}
	newRecord := Record[K, V]{pk, values}
	me.m[pk] = values
	me.single.Add(newRecord)
	return
}

func (me *Map[K, V]) Iter() iter.Seq[Record[K, V]] {
	return me.single.Iter()
}

func (me *Map[K, V]) IterPrimaryKeys() iter.Seq[K] {
	return func(yield func(K) bool) {
		for r := range me.Iter() {
			if !yield(r.PrimaryKey) {
				return
			}
		}
	}
}

func (me *Map[K, V]) Get(pk K) g.Option[V] {
	v, ok := me.m[pk]
	return g.OptionFromTuple(v, ok)
}

type Iterator[K, V any] = btree.SetIterator[Record[K, V]]

func (me *Map[K, V]) Iterator() Iterator[K, V] {
	return Iterator[K, V](me.single.index.Iterator())
}
