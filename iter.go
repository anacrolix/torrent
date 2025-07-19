package torrent

import (
	"iter"

	"golang.org/x/exp/constraints"
)

// Returns an iterator that yields integers from start (inclusive) to end (exclusive).
func iterRange[T constraints.Integer](start, end T) iter.Seq[T] {
	return func(yield func(T) bool) {
		for i := start; i < end; i++ {
			if !yield(i) {
				return
			}
		}
	}
}
