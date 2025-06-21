package atomicx

import (
	"sync"
	"sync/atomic"
)

func Pointer[T any](v T) (r *atomic.Pointer[T]) {
	r = &atomic.Pointer[T]{}
	r.Store(&v)
	return r
}

func NewBoolCond() BoolCond {
	return BoolCond{
		Bool: new(atomic.Bool),
		Cond: sync.NewCond(&sync.Mutex{}),
		c:    make(chan bool),
	}
}

type BoolCond struct {
	*atomic.Bool
	*sync.Cond
	c chan bool
}

// func init() {
// 	var b BoolCond = NewBoolCond()
// }
