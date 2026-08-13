//go:build go1.25

package torrent

import (
	"log/slog"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/sync"
	"github.com/go-quicktest/qt"
)

// This test doesn't really do much useful anymore. It is useful to break apart the dispatcher a bit
// for testing. It's good to have something that hits up the triggers a bit.
func TestUpdateOverdueRecursion(t *testing.T) {
	// Prevent synctest from tracking some stuff that we don't care about.
	cl := newTestingClient(t)
	synctest.Test(t, func(t *testing.T) {
		d := regularTrackerAnnounceDispatcher{}
		d.initTables()
		d.initTimerNoop()
		d.logger = slog.Default()
		u, _ := url.Parse("http://derp")
		d.initTrackerClient(u, trackerAnnouncerKey(u.String()), cl.config, slog.Default())
		// Two values. One that needs to be marked not overdue on the first call to updateOverdue,
		// and the other that is by a recursive call, and subsequently reversed when we bounce back
		// out to the original call.
		key1 := torrentTrackerAnnouncerKey{}
		key1.ShortInfohash[0] = 1
		key2 := torrentTrackerAnnouncerKey{}
		key2.ShortInfohash[0] = 2
		value1 := nextAnnounceInput{}
		value1.overdue = false
		value1.When = time.Now()
		value2 := nextAnnounceInput{}
		value2.overdue = true
		value2.When = time.Now().Add(4)
		println(value1.When.UnixNano(), value2.When.UnixNano())
		panicif.False(d.announceData.Create(key1, value1))
		panicif.False(d.announceData.Create(key2, value2))
		v2, ok := d.announceData.Get(key2)
		panicif.False(ok)
		expectedValue2 := value2
		expectedValue2.overdue = false
		qt.Check(t, qt.Equals(v2, expectedValue2))
		println(time.Now().UnixNano())
		// This will fix up the values. But if we can advance time and trigger a recursive
		// updateOverdue we can test for thrashing, but it's non-trivial.
		d.updateOverdue()
	})
}

func TestDropTorrentClearsPendingTrackerInputUpdate(t *testing.T) {
	cfg := TestingConfig(t)
	qt.Assert(t, qt.IsTrue(cfg.DisableTrackers))
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() {
		qt.Check(t, qt.HasLen(cl.Close(), 0))
	})

	trackerURL := "http://tracker.invalid/announce"
	u, err := url.Parse(trackerURL)
	qt.Assert(t, qt.IsNil(err))

	var opts AddTorrentOpts
	opts.InfoHash[0] = 1
	tt, new := cl.AddTorrentOpt(opts)
	qt.Assert(t, qt.IsTrue(new))

	d := &cl.regularTrackerAnnounceDispatcher
	key := torrentTrackerAnnouncerKey{
		ShortInfohash: *tt.canonicalShortInfohash(),
		url:           trackerAnnouncerKey(trackerURL),
	}

	var wg sync.WaitGroup
	cl.lock()
	defer func() {
		cl.unlock()
		wg.Wait()
	}()

	d.initTrackerClient(u, key.url, cl.config, slog.Default())
	tt.initRegularTrackerAnnounceState(key)
	_, ok := d.pendingTorrentInputUpdates[tt]
	qt.Assert(t, qt.IsTrue(ok))

	tt.close(&wg)
	_, ok = d.pendingTorrentInputUpdates[tt]
	qt.Check(t, qt.IsFalse(ok))
}
