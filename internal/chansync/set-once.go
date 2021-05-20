package chansync

import "sync"

// SetOnce is a boolean value that can only be flipped from false to true.
type SetOnce struct {
	ch        chan struct{}
	initOnce  sync.Once
	closeOnce sync.Once
}

func (me *SetOnce) Chan() <-chan struct{} {
	me.init()
	return me.ch
}

func (me *SetOnce) init() {
	me.initOnce.Do(func() {
		me.ch = make(chan struct{})
	})
}

// Set only returns true the first time it is called.
func (me *SetOnce) Set() (first bool) {
	me.closeOnce.Do(func() {
		me.init()
		first = true
		close(me.ch)
	})
	return
}

func (me *SetOnce) IsSet() bool {
	me.init()
	select {
	case <-me.ch:
		return true
	default:
		return false
	}
}
