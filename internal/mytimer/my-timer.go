package mytimer

import (
	"math"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/sync"
)

// Very common pattern I have in my code, a timer that coordinates resets from the callback
// function, externally, and tracks when it's due. Only one instance of the timer callback can be
// running at a time, and that callback is responsible for also returning the next delay.
type Timer struct {
	mu     sync.RWMutex
	when   TimeValue
	f      Func
	t      *time.Timer
	inited bool
}

func (me *Timer) Init(first TimeValue, f Func) {
	panicif.True(me.inited)
	me.inited = true
	me.f = f
	me.when = first
	d := time.Until(first.Time)
	if first.IsZero() {
		d = math.MaxInt64
	}
	// Should we Stop the timer if there's no initial delay set? We can't update the timer
	// externally unless we know if the timer is scheduled.
	me.t = time.AfterFunc(d, me.innerCallback)
}

func (me *Timer) update(when TimeValue) {
	me.mu.Lock()
	defer me.mu.Unlock()
	// Avoid hammering the scheduler with changes. I think it's cheaper to hit the Timer mutex than
	// it is to reset the timer unnecessarily.
	if effectivelyEq(when, me.when) {
		return
	}
	if !me.t.Stop() {
		// Timer callback might be active.
		if !me.when.IsZero() {
			// Timer callback is running.
			return
		}
	}
	me.reset(when)
}

func (me *Timer) reset(when TimeValue) {
	me.when = when
	if !me.when.IsZero() {
		panicif.True(me.t.Reset(time.Until(me.when.Time)))
	}
}
func (me *Timer) When() TimeValue {
	return me.when
}

func (me *Timer) innerCallback() {
	panicif.True(me.when.IsZero())
	panicif.True(me.when.After(time.Now()))
	// Nobody else can set it while we're running (when is non-zero and the timer has fired
	// already).
	when := me.f()
	me.mu.Lock()
	me.reset(when)
	me.mu.Unlock()
}

// Resets if the timer func hasn't fired.
func (me *Timer) Update(when TimeValue) {
	me.update(when)
}
