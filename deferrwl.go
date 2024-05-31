package torrent

import (
	"sync/atomic"

	"github.com/anacrolix/sync"
	stack2 "github.com/go-stack/stack"
)

// Runs deferred actions on Unlock. Note that actions are assumed to be the results of changes that
// would only occur with a write lock at present. The race detector should catch instances of defers
// without the write lock being held.
type lockWithDeferreds struct {
	internal      sync.RWMutex
	unlockActions []func()

	lc     atomic.Int32
	locker string
}

func stack(skip int) string {
	return stack2.Trace().TrimBelow(stack2.Caller(skip)).String()
}

func (me *lockWithDeferreds) Lock() {
	me.internal.Lock()
	me.lc.Add(1)
	// me.locker = stack(2)
}

func (me *lockWithDeferreds) Unlock() {
	me.lc.Add(-1)
	if me.lc.Load() < 0 {
		panic("lock underflow")
	}
	me.locker = ""
	unlockActions := me.unlockActions
	for i := 0; i < len(unlockActions); i += 1 {
		unlockActions[i]()
	}
	me.unlockActions = unlockActions[:0]
	me.internal.Unlock()
}

func (me *lockWithDeferreds) RLock() {
	me.internal.RLock()
}

func (me *lockWithDeferreds) RUnlock() {
	me.internal.RUnlock()
}

func (me *lockWithDeferreds) Defer(action func()) {
	me.unlockActions = append(me.unlockActions, action)
}
