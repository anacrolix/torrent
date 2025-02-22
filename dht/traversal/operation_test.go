package traversal

import (
	"testing"
	"time"
)

func TestDeferClose(t *testing.T) {
	t.Parallel()
	ch := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(ch)
		<-done
	}()
	select {
	case <-ch:
		t.Fatal("deferred close occurred prematurely")
	case <-time.After(time.Second):
	}
	close(done)
	<-ch
}
