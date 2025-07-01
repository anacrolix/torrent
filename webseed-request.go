package torrent

import (
	"fmt"

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
	cancelled bool
}

func (me *webseedRequest) Close() {
	me.request.Cancel()
}

// Record that it was exceptionally cancelled.
func (me *webseedRequest) Cancel() {
	me.request.Cancel()
	if !me.cancelled {
		me.cancelled = true
		if webseed.PrintDebug {
			fmt.Printf("cancelled webseed request\n")
		}
	}
}
