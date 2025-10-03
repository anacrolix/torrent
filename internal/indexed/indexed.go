package indexed

import (
	"iter"

	"github.com/ajwerner/btree"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type Record[K, V any] struct {
	PrimaryKey K
	Values     V
}

// Not sure if this is specific to map or something else yet. But it's a pattern I keep using.
type indexImpl[R any] struct {
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
	single index[Record[K, V]]
}

type index[R any] interface {
	Delete(r R) bool
	Add(r R)
	Compare(R, R) int
	Iter() iter.Seq[R]
}

type IndexHelper[R any] interface {
	Compare(R, R) int
}

// Single index map
func NewMap[K comparable, V any](
	cmp func(Record[K, V], Record[K, V]) int,
) *Map[K, V] {
	ret := &Map[K, V]{
		m: make(map[K]V),
		single: &indexImpl[Record[K, V]]{
			index: btree.MakeSet[Record[K, V]](cmp),
			cmp:   cmp,
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

func (me *Map[K, V]) Get(pk K) g.Option[V] {
	v, ok := me.m[pk]
	return g.OptionFromTuple(v, ok)
}
