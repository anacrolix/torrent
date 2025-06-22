package atomicx

import (
	"sync/atomic"
)

func Pointer[T any](v T) (r *atomic.Pointer[T]) {
	r = &atomic.Pointer[T]{}
	r.Store(&v)
	return r
}
