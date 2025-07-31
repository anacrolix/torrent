package torrent

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/anacrolix/torrent/webseed"
)

// A wrapper around webseed.Request with extra state for webseedPeer.
type webseedRequest struct {
	// Fingers out.
	request webseed.Request
	logger  *slog.Logger
	// First assigned in the range.
	begin RequestIndex
	// The next to be read.
	next RequestIndex
	// One greater than the end of the range.
	end       RequestIndex
	cancelled atomic.Bool
}

func (me *webseedRequest) Close() {
	me.request.Close()
}

// Record that it was exceptionally cancelled.
func (me *webseedRequest) Cancel(reason string) {
	me.request.Cancel()
	if !me.cancelled.Swap(true) {
		if webseed.PrintDebug {
			me.logger.Debug("cancelled", "reason", reason)
		}
	}
}

func (me *webseedRequest) String() string {
	s := fmt.Sprintf("%v of [%v-%v)", me.next, me.begin, me.end)
	if me.cancelled.Load() {
		s += " (cancelled)"
	}
	return s
}
