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

// Record that it was exceptionally cancelled. Require that Torrent be passed so we can ensure
// announce dispatcher is updated.
func (me *webseedRequest) Cancel(cause string, t *Torrent) bool {
	me.request.Cancel(stringError(cause))
	if !me.cancelled.Swap(true) {
		if webseed.PrintDebug {
			me.logger.Debug("webseed request cancelled", "cause", cause)
		}
		t.deferUpdateRegularTrackerAnnouncing()
		return true
	}
	return false
}

func (me *webseedRequest) String() string {
	s := fmt.Sprintf("%v of [%v-%v)", me.next, me.begin, me.end)
	if me.cancelled.Load() {
		s += " (cancelled)"
	}
	return s
}
