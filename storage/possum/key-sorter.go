package possumTorrentStorage

import (
	"cmp"
)

// Sorts by a precomputed key but swaps on another slice at the same time.
type keySorter[T any, K cmp.Ordered] struct {
	orig []T
	keys []K
}

func (o keySorter[T, K]) Len() int {
	return len(o.keys)
}

func (o keySorter[T, K]) Less(i, j int) bool {
	return o.keys[i] < o.keys[j]
}

func (o keySorter[T, K]) Swap(i, j int) {
	o.keys[i], o.keys[j] = o.keys[j], o.keys[i]
	o.orig[i], o.orig[j] = o.orig[j], o.orig[i]
}
