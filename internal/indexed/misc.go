package indexed

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/option"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type InsteadOf[R any] func(old, new g.Option[R]) g.Option[R]

type triggerFunc[R any] func(old, new g.Option[R])

type CompareFunc[T any] func(a, b T) int

type Pair[K, V any] struct {
	Left  K
	Right V
}

func NewPair[K, V any](left K, right V) Pair[K, V] {
	return Pair[K, V]{Left: left, Right: right}
}

func (me Pair[K, V]) Flip() Pair[V, K] {
	return Pair[V, K]{Left: me.Right, Right: me.Left}
}

func pairMapRight[K, V any](me Pair[K, V]) V {
	return me.Right
}

// A full index that doesn't require mapping records.
func NewFullIndex[R comparable](from tableInterface[R], cmpFunc CompareFunc[R], minRecord R) (index Index[R]) {
	return NewFullMappedIndex(
		from,
		cmpFunc,
		func(r R) R { return r },
		minRecord,
	)
}

// An index on a mapped form of the upstream data. What about cross-table and partial indexes?
func NewFullMappedIndex[F, T comparable](
	from tableInterface[F],
	cmpFunc CompareFunc[T],
	mapFunc func(F) T,
	minRecord T,
) Index[T] {
	var index *table[T]
	g.InitNew(&index)
	index.Init(cmpFunc)
	index.SetMinRecord(minRecord)
	from.addIndex(
		index,
		func(old, new g.Option[F]) {
			index.Change(option.Map(mapFunc, old), option.Map(mapFunc, new))
			panicif.NotEq(from.Len(), index.Len())
		},
	)
	return index
}

func (me *table[R]) addIndex(index genericRelation, trigger triggerFunc[R]) {
	me.indexes = append(me.indexes, index)
	me.indexTriggers = append(me.indexTriggers, trigger)
}
