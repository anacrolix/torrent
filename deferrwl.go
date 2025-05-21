package torrent

import (
	"fmt"

	"github.com/anacrolix/sync"
)

// Runs deferred actions on Unlock. Note that actions are assumed to be the results of changes that
// would only occur with a write lock at present. The race detector should catch instances of defers
// without the write lock being held.
type lockWithDeferreds struct {
	internal      sync.RWMutex
	unlockActions []func()
}

func (me *lockWithDeferreds) Lock() {
	me.internal.Lock()
}

func (me *lockWithDeferreds) Unlock() {
	defer me.internal.Unlock()
	startLen := len(me.unlockActions)
	for i := range startLen {
		me.unlockActions[i]()
	}
	if len(me.unlockActions) != startLen {
		panic(fmt.Sprintf("num deferred changed while running: %v -> %v", startLen, len(me.unlockActions)))
	}
	me.unlockActions = me.unlockActions[:0]
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
