package torrent

import (
	"sync/atomic"
	"time"
)

// A time.AfterFunc timer that drops its function when stopped. The Go runtime can keep a stopped
// timer in its timer heap until the time it was due to fire, and the timer keeps its function
// alive until then. Functions that capture a PeerConn would otherwise keep the PeerConn, and its
// Torrent, from being collected for the rest of the timer's delay (up to pexInterval for PEX).
type releasedTimer struct {
	timer *time.Timer
	f     atomic.Pointer[func()]
}

func newReleasedTimer(d time.Duration, f func()) *releasedTimer {
	rt := &releasedTimer{}
	rt.f.Store(&f)
	// The runtime timer must only reference rt, not f directly.
	rt.timer = time.AfterFunc(d, rt.run)
	return rt
}

func (rt *releasedTimer) run() {
	if f := rt.f.Load(); f != nil {
		(*f)()
	}
}

// Has no effect after Stop.
func (rt *releasedTimer) Reset(d time.Duration) {
	if rt.f.Load() == nil {
		return
	}
	rt.timer.Reset(d)
}

// Stops the timer for good and releases its function. A call to the function that is already
// running is not interrupted.
func (rt *releasedTimer) Stop() {
	rt.f.Store(nil)
	rt.timer.Stop()
}
