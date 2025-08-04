package alloclim

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/log"
)

type Reservation struct {
	l           *Limiter
	n           int64
	releaseOnce sync.Once
	mu          sync.Mutex
	granted     chansync.SetOnce
	cancelled   chansync.SetOnce
}

// Releases the alloc claim if the reservation has been granted. Does nothing if it was cancelled.
// Otherwise panics.
func (me *Reservation) Release() {
	me.mu.Lock()
	defer me.mu.Unlock()
	switch {
	default:
		panic("not resolved")
	case me.cancelled.IsSet():
		return
	case me.granted.IsSet():
	}
	me.releaseOnce.Do(func() {
		me.l.addValue(me.n)
	})
}

// Cancel the reservation, returns false if it was already granted. You must still release if that's
// the case. See Drop.
func (me *Reservation) Cancel() bool {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.granted.IsSet() {
		return false
	}
	if me.cancelled.Set() {
		go me.l.doWakes()
	}
	return true
}

// If the reservation is granted, release it, otherwise cancel the reservation. Returns true if the
// reservation was released or cancelled for the first time.
func (me *Reservation) Drop() (first bool) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.granted.IsSet() {
		me.releaseOnce.Do(func() {
			first = true
			me.l.addValue(me.n)
		})
		return
	}
	if me.cancelled.Set() {
		first = true
		go me.l.doWakes()
	}
	return
}

func (me *Reservation) wake() bool {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.cancelled.IsSet() {
		return false
	}
	return me.granted.Set()
}

func (me *Reservation) Wait(ctx context.Context) error {
	if me.n > me.l.Max {
		return log.WithLevel(
			log.Warning,
			fmt.Errorf("reservation for %v exceeds limiter max %v", me.n, me.l.Max),
		)
	}
	select {
	case <-ctx.Done():
	case <-me.granted.Done():
	case <-me.cancelled.Done():
	}
	defer me.mu.Unlock()
	me.mu.Lock()
	switch {
	case me.granted.IsSet():
		return nil
	case me.cancelled.IsSet():
		return errors.New("reservation cancelled")
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		panic("unexpected")
	}
}
