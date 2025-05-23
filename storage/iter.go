package storage

import (
	"iter"
)

func enumIter[T any](i iter.Seq[T]) iter.Seq2[int, T] {
	return func(yield func(int, T) bool) {
		j := 0
		for t := range i {
			if !yield(j, t) {
				return
			}
			j++
		}
	}
}
