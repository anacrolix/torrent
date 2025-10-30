package indexed

import (
	"iter"
)

func MapPairIterRight[K, V any](i iter.Seq[Pair[K, V]]) iter.Seq[V] {
	return func(yield func(V) bool) {
		for p := range i {
			if !yield(p.Right) {
				return
			}
		}
	}
}

func MapPairIterLeft[K, V any](i iter.Seq[Pair[K, V]]) iter.Seq[K] {
	return func(yield func(K) bool) {
		for p := range i {
			if !yield(p.Left) {
				return
			}
		}
	}
}
