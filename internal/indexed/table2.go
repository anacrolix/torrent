package indexed

import (
	"iter"
)

type Table2[K, V any] struct {
	table[MapRecord[K, V]]
}

func (me *Table2[K, V]) IterKeysFrom(start K) iter.Seq[K] {
	return func(yield func(K) bool) {
		for mr := range me.table.IterFrom(MapRecord[K, V]{Key: start}) {
			if !yield(mr.Key) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) IterKeyRange(start, end K) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for r := range me.table.IterRange(MapRecord[K, V]{Key: start}, MapRecord[K, V]{Key: end}) {
			if !yield(r.Key, r.Value) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) Iter() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for r := range me.table.Iter() {
			if !yield(r.Key, r.Value) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) IterFirst() iter.Seq[K] {
	return func(yield func(K) bool) {
		for first := range me.Iter() {
			if !yield(first) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) IterSecond() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, second := range me.Iter() {
			if !yield(second) {
				return
			}
		}
	}
}

func (me *Table2[K, V]) Create(first K, second V) bool {
	return me.table.Create(MapRecord[K, V]{first, second})
}

// Beginnings of an index/map type?
func (me *Table2[K, V]) IterFirstRangeToSecond(gte, lt K) iter.Seq[V] {
	return func(yield func(V) bool) {
		for r := range me.IterRange(MapRecord[K, V]{Key: gte}, MapRecord[K, V]{Key: lt}) {
			if !yield(r.Value) {
				return
			}
		}
	}
}
