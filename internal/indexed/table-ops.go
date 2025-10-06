package indexed

import (
	"cmp"
)

type TableOps[K PrimaryKey[K], R any] struct {
	PrimaryKey        func(*R) *K
	OnModified        func(R) R
	ComparePrimaryKey func(a, b K) int
}

// Compares using the index ordering, and then uses the primary key to break ties.
func (me TableOps[K, R]) indexFullCompare(indexCmp RecordCmpFunc[R]) RecordCmpFunc[R] {
	return func(a, b R) int {
		return cmp.Or(indexCmp(a, b), me.ComparePrimaryKey(*me.PrimaryKey(&a), *me.PrimaryKey(&b)))
	}
}
