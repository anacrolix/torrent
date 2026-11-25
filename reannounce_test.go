package torrent

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// Reannounce asks a regular tracker again now, whatever interval it asked for, with a regular
// announce (not a second Started), and only once.
func TestReannounce(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	url, events := newEventTracker(t) // asks for 30 minutes
	tor, _ := cl.AddTorrentInfoHash(metainfo.Hash{1, 2, 3})
	tor.AddTrackers([][]string{{url}})
	requireAnnounceEvent(t, events, "started")
	announced := func() bool {
		cl.lock()
		defer cl.unlock()
		for _, state := range tor.regularTrackerAnnounceState {
			if !state.lastOk.Completed.IsZero() {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(10 * time.Second); !announced(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("started announce never completed")
		}
	}

	tor.Reannounce()
	requireAnnounceEvent(t, events, "")
	select {
	case ev := <-events:
		t.Fatalf("announced again: %q", ev)
	case <-time.After(time.Second):
	}
}

// Reannounce on a torrent without trackers, or a dropped one, does nothing.
func TestReannounceWithNothingToDo(t *testing.T) {
	cl := newTestingClient(t)
	tor, _ := cl.AddTorrentInfoHash(metainfo.Hash{4, 5, 6})
	tor.Reannounce()
	tor.Drop()
	tor.Reannounce()
}
