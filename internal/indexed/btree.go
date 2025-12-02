package indexed

import (
	"iter"
)

type btreeSet[R any] interface {
	Iter(yield func(R) bool)
	IterFrom(start R) iter.Seq[R]
	Delete(r R) (actual R, removed bool)
	Upsert(r R) (_ R, overwrote bool)
	Contains(R) bool
	Len() int
}
