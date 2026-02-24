package indexed

import (
	"iter"

	g "github.com/anacrolix/generics"
)

type Table2[K, V comparable] struct {
	*Table[Pair[K, V]]
}

func (me *Table2[K, V]) Init(cmpFunc CompareFunc[Pair[K, V]]) {
	g.InitNew(&me.Table)
	me.Table.Init(cmpFunc)
}

func (me *Table2[K, V]) IterKeysFrom(start K) iter.Seq[K] {
	return func(yield func(K) bool) {
		for mr := range me.table.IterFrom(Pair[K, V]{Left: start}) {
			if !yield(mr.Left) {
				return
			}
		}
	}
}

func Iter2[K, V any](me tableInterface[Pair[K, V]]) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for r := range me.Iter {
			if !yield(r.Left, r.Right) {
				return
			}
		}
	}
}

func IterFirst[K, V any](me tableInterface[Pair[K, V]]) iter.Seq[K] {
	return func(yield func(K) bool) {
		for first := range Iter2(me) {
			if !yield(first) {
				return
			}
		}
	}
}

func IterSecond[K, V any](me tableInterface[Pair[K, V]]) iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, second := range Iter2(me) {
			if !yield(second) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) Create(first K, second V) bool {
	return me.table.Create(Pair[K, V]{first, second})
}
