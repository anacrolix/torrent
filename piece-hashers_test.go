package torrent

import (
	"testing"
	"time"
)

// Sets the limit on hashers for a test. Hashers read it under the client lock, and the test waits
// for them to finish before putting it back, so no hasher outlives the test.
func setMaxActivePieceHashers(t *testing.T, cl *Client, n int) {
	cl.lock()
	old := maxActivePieceHashers
	maxActivePieceHashers = n
	cl.unlock()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			cl.lock()
			maxActivePieceHashers = old
			cl.startPieceHashers()
			idle := cl.activePieceHashers == 0
			cl.unlock()
			if idle || time.Now().After(deadline) {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}
