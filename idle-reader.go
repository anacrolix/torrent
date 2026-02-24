package torrent

import (
	"fmt"
	"io"
	"time"
)

func newIdleTimeoutReader(r io.Reader, timeout time.Duration, interrupt func()) io.Reader {
	tr := &idleTimeoutReader{
		r:         r,
		interrupt: interrupt,
		timeout:   timeout,
	}
	tr.timer = time.AfterFunc(timeout, tr.timerFunc)
	tr.timer.Stop()
	return tr
}

type idleTimeoutReader struct {
	r         io.Reader
	interrupt func()
	timer     *time.Timer
	timeout   time.Duration
	fired     bool
}

func (me *idleTimeoutReader) Read(p []byte) (n int, err error) {
	// Allow resuming reading even after a previous idle timeout.
	me.fired = false
	me.timer.Reset(me.timeout)
	n, err = me.r.Read(p)
	me.timer.Stop()
	if me.fired {
		// Wrap the error, we don't know if the interrupt caused the inner reader to return early.
		err = fmt.Errorf("idle timeout, original error: %w", err)
	}
	return
}

func (me *idleTimeoutReader) timerFunc() {
	me.fired = true
	me.interrupt()
}
