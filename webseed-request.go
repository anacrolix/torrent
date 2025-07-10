package torrent

import (
	"fmt"
	"sync/atomic"

	"github.com/anacrolix/torrent/webseed"
)

// A wrapper around webseed.Request with extra state for webseedPeer.
type webseedRequest struct {
	// Fingers out.
	request webseed.Request
	// First assigned in the range.
	begin RequestIndex
	// The next to be read.
	next RequestIndex
	// One greater than the end of the range.
	end       RequestIndex
	cancelled atomic.Bool
}

func (me *webseedRequest) Close() {
	me.request.Cancel()
}

// Record that it was exceptionally cancelled.
func (me *webseedRequest) Cancel() {
	me.request.Cancel()
	if !me.cancelled.Swap(true) {
		if webseed.PrintDebug {
			fmt.Printf("cancelled webseed request\n")
		}
	}
}
