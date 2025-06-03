package torrent

import (
	"fmt"
	"reflect"

	g "github.com/anacrolix/generics"
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
	unlocking bool
}

func (me *lockWithDeferreds) Lock() {
	me.internal.Lock()
}

func (me *lockWithDeferreds) Unlock() {
	me.unlocking = true
	startLen := len(me.unlockActions)
	var i int
	for i = 0; i < len(me.unlockActions); i++ {
		me.unlockActions[i]()
	}
	if i != len(me.unlockActions) {
		panic(fmt.Sprintf("num deferred changed while running: %v -> %v", startLen, len(me.unlockActions)))
	}
	me.unlockActions = me.unlockActions[:0]
	clear(me.uniqueActions)
	me.unlocking = false
	me.internal.Unlock()
}

func (me *lockWithDeferreds) RLock() {
	me.internal.RLock()
}

func (me *lockWithDeferreds) RUnlock() {
	me.internal.RUnlock()
}

// Not allowed after unlock has started.
func (me *lockWithDeferreds) Defer(action func()) {
	if me.unlocking {
		panic("defer called while unlocking")
	}
	me.deferInner(action)
}

// Already guarded.
func (me *lockWithDeferreds) deferInner(action func()) {
	me.unlockActions = append(me.unlockActions, action)
}

// Protected from looping by once filter.
func (me *lockWithDeferreds) deferOnceInner(key any, action func()) {
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
