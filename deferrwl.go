package torrent

import (
	"fmt"
	"reflect"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/sync"
)

// Runs deferred actions on Unlock. Note that actions are assumed to be the results of changes that
// would only occur with a write lock at present. The race detector should catch instances of defers
// without the write lock being held.
type lockWithDeferreds struct {
	internal      sync.RWMutex
	unlockActions []func()
	uniqueActions map[any]struct{}
	// Currently unlocking, defers should not occur?
	allowDefers bool
	client      *Client
}

func (me *lockWithDeferreds) Lock() {
	me.internal.Lock()
	panicif.True(me.allowDefers)
	me.allowDefers = true
}

func (me *lockWithDeferreds) Unlock() {
	// If this doesn't happen other clean up handlers will block on the lock.
	defer me.internal.Unlock()
	panicif.False(me.allowDefers)
	me.allowDefers = false
	me.client.unlockHandlers.run(me.client.slogger)
	startLen := len(me.unlockActions)
	var i int
	for i = 0; i < len(me.unlockActions); i++ {
		me.unlockActions[i]()
	}
	if i != len(me.unlockActions) {
		panic(fmt.Sprintf("num deferred changed while running: %v -> %v", startLen, len(me.unlockActions)))
	}
	me.unlockActions = me.unlockActions[:0]
	me.uniqueActions = nil
}

func (me *lockWithDeferreds) RLock() {
	me.internal.RLock()
}

func (me *lockWithDeferreds) RUnlock() {
	me.internal.RUnlock()
}

// Not allowed after unlock has started.
func (me *lockWithDeferreds) Defer(action func()) {
	me.deferInner(action)
}

// Already guarded.
func (me *lockWithDeferreds) deferInner(action func()) {
	panicif.False(me.allowDefers)
	me.unlockActions = append(me.unlockActions, action)
}

// Protected from looping by once filter.
func (me *lockWithDeferreds) deferOnceInner(key any, action func()) {
	panicif.False(me.allowDefers)
	g.MakeMapIfNil(&me.uniqueActions)
	if g.MapContains(me.uniqueActions, key) {
		return
	}
	me.uniqueActions[key] = struct{}{}
	me.deferInner(action)
}

// Protected from looping by once filter. Note that if arg is the receiver of action, it should
// match the receiver type (like being a pointer if the method takes a pointer receiver).
func (me *lockWithDeferreds) DeferUniqueUnaryFunc(arg any, action func()) {
	me.deferOnceInner(unaryFuncKey(action, arg), action)
}

func unaryFuncKey(f func(), key any) funcAndArgKey {
	return funcAndArgKey{
		funcStr: reflect.ValueOf(f).String(),
		key:     key,
	}
}

type funcAndArgKey struct {
	funcStr string
	key     any
}
