package alloclim

import "sync"

// Manages reservations sharing a common allocation limit.
type Limiter struct {
	// Maximum outstanding allocation space.
	Max      int64
	initOnce sync.Once
	mu       sync.Mutex
	// Current unallocated space.
	value int64
	// Reservations waiting to in the order they arrived.
	waiting []*Reservation
}

func (me *Limiter) initValue() {
	me.value = me.Max
}

func (me *Limiter) init() {
	me.initOnce.Do(func() {
		me.initValue()
	})
}

func (me *Limiter) Reserve(n int64) *Reservation {
	r := &Reservation{
		l: me,
		n: n,
	}
	me.init()
	me.mu.Lock()
	if n <= me.value {
		me.value -= n
		r.granted.Set()
	} else {
		me.waiting = append(me.waiting, r)
	}
	me.mu.Unlock()
	return r
}

func (me *Limiter) doWakesLocked() {
	for {
		if len(me.waiting) == 0 {
			break
		}
		r := me.waiting[0]
		switch {
		case r.cancelled.IsSet():
		case r.n <= me.value:
			if r.wake() {
				me.value -= r.n
			}
		default:
			return
		}
		me.waiting = me.waiting[1:]
	}
}

func (me *Limiter) doWakes() {
	me.mu.Lock()
	me.doWakesLocked()
	me.mu.Unlock()
}

func (me *Limiter) addValue(n int64) {
	me.mu.Lock()
	me.value += n
	me.doWakesLocked()
	me.mu.Unlock()
}

func (me *Limiter) Value() int64 {
	me.mu.Lock()
	defer me.mu.Unlock()
	return me.value
}
