//go:build go1.25

package torrent

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/tracker"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Adding a torrent with a v2 infohash but no info bytes used to panic in
// updateTorrentInput: addKey can't find the torrent by its v2 short hash
// (not yet in torrentsByShortHash), returns early, but
// initRegularTrackerAnnounceState still records the key and defers an
// update. The subsequent updateTorrentInput then hits a key missing from
// announceData. See #1068.
func TestV2InfohashUpdateTorrentInputNoPanic(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	infoBytes := []byte("d6:lengthi1e4:name1:x12:piece lengthi16384e6:pieces20:aaaaaaaaaaaaaaaaaaaae")
	v2 := infohash_v2.HashBytes(infoBytes)
	torr, new_ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:   metainfo.HashBytes(infoBytes),
		InfoHashV2: g.Some(v2),
	})
	qt.Assert(t, qt.IsTrue(new_))
	cl.lock()
	defer cl.unlock()
	torr.addTrackers([][]string{{"http://tracker.example.com:6969/announce"}})
	cl.regularTrackerAnnounceDispatcher.updateTorrentInput(torr)
}

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

// A dispatcher with numTorrents infohashes announcing to each of numTrackers trackers, all due.
func newTestDispatcher(tb testing.TB, numTorrents, numTrackers int) *regularTrackerAnnounceDispatcher {
	return newTestDispatcherDue(tb, numTorrents, numTrackers, numTrackers)
}

// Like newTestDispatcher, but only the first dueTrackers are due; the rest wait, as failing
// trackers do in their backoff.
func newTestDispatcherDue(tb testing.TB, numTorrents, numTrackers, dueTrackers int) *regularTrackerAnnounceDispatcher {
	cl := newTestingClient(tb)
	d := &regularTrackerAnnounceDispatcher{}
	d.initTables()
	d.initTimerNoop()
	d.logger = slog.Default()
	for tr := range numTrackers {
		u, _ := url.Parse(fmt.Sprintf("http://tracker%d.example", tr))
		d.initTrackerClient(u, trackerAnnouncerKey(u.String()), cl.config, slog.Default())
	}
	for ih := range numTorrents {
		for tr := range numTrackers {
			var key torrentTrackerAnnouncerKey
			binary.BigEndian.PutUint32(key.ShortInfohash[:], uint32(ih)+1)
			key.url = trackerAnnouncerKey(fmt.Sprintf("http://tracker%d.example", tr))
			var input nextAnnounceInput
			input.When = time.Now().Add(-time.Minute)
			if tr >= dueTrackers {
				input.When = time.Now().Add(time.Hour)
			}
			input.AnnounceEvent = tracker.Started
			panicif.False(d.announceData.Create(key, input))
		}
	}
	return d
}

func TestInfohashBusyFollowsAnnounceConcurrency(t *testing.T) {
	d := newTestDispatcher(t, 2, 3)
	var ih shortInfohash
	binary.BigEndian.PutUint32(ih[:], 1)
	busyRows := func() (n int) {
		for p := range d.announceData.Iter {
			if p.Right.infohashBusy {
				qt.Assert(t, qt.Equals(p.Left.ShortInfohash, ih))
				n++
			}
		}
		return
	}
	add := func(delta int) {
		d.alterInfohashConcurrency(ih, func(n int) int { return n + delta })
	}
	add(1)
	qt.Check(t, qt.Equals(busyRows(), 3))
	add(1)
	qt.Check(t, qt.Equals(busyRows(), 3))
	add(-1)
	qt.Check(t, qt.Equals(busyRows(), 3))
	add(-1)
	qt.Check(t, qt.Equals(busyRows(), 0))
}

func TestInfohashBusyReachesWaitingRowsWhenTheyChange(t *testing.T) {
	d := newTestDispatcherDue(t, 1, 3, 1)
	var ih shortInfohash
	binary.BigEndian.PutUint32(ih[:], 1)
	busy := func(tr int) bool {
		key := torrentTrackerAnnouncerKey{ShortInfohash: ih, url: trackerAnnouncerKey(fmt.Sprintf("http://tracker%d.example", tr))}
		v, ok := d.announceData.Get(key)
		qt.Assert(t, qt.IsTrue(ok))
		return v.infohashBusy
	}
	d.alterInfohashConcurrency(ih, func(n int) int { return n + 1 })
	qt.Check(t, qt.IsTrue(busy(0)))
	qt.Check(t, qt.IsFalse(busy(1)), qt.Commentf("a waiting row is not reindexed"))
	key := torrentTrackerAnnouncerKey{ShortInfohash: ih, url: "http://tracker1.example"}
	d.announceData.Update(key, func(v nextAnnounceInput) nextAnnounceInput {
		v.When = time.Now().Add(-time.Second)
		return v
	})
	qt.Check(t, qt.IsTrue(busy(1)), qt.Commentf("a row that becomes due takes up busy"))
}

// Most trackers of a torrent fail and wait in their backoff; only a few are due.
func BenchmarkInfohashAnnounceConcurrencyMostWaiting(b *testing.B) {
	const numTorrents, numTrackers, dueTrackers = 250, 40, 4
	d := newTestDispatcherDue(b, numTorrents, numTrackers, dueTrackers)
	b.ResetTimer()
	for i := range b.N {
		var ih shortInfohash
		binary.BigEndian.PutUint32(ih[:], uint32(i%numTorrents)+1)
		d.alterInfohashConcurrency(ih, func(n int) int { return n + 1 })
		d.alterInfohashConcurrency(ih, func(n int) int { return n - 1 })
	}
}

// Each torrent announces to all its trackers at once, as a newly added torrent does, and the
// announces finish one after the other.
func BenchmarkInfohashAnnounceConcurrency(b *testing.B) {
	const numTorrents, numTrackers = 250, 40
	d := newTestDispatcher(b, numTorrents, numTrackers)
	b.ResetTimer()
	for i := range b.N {
		var ih shortInfohash
		binary.BigEndian.PutUint32(ih[:], uint32(i%numTorrents)+1)
		for range numTrackers {
			d.alterInfohashConcurrency(ih, func(n int) int { return n + 1 })
		}
		for range numTrackers {
			d.alterInfohashConcurrency(ih, func(n int) int { return n - 1 })
		}
	}
}
