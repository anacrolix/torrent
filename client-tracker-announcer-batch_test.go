package torrent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// Announces that finish together must not each queue for the Client lock: one of them takes it
// and applies the others' results too.
func TestFinishedAnnouncesAreAppliedTogether(t *testing.T) {
	const numTrackers = 8
	release := make(chan struct{})
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Add(1)
		<-release
		w.Write([]byte("d8:completei1e10:incompletei1e8:intervali600e5:peers0:e"))
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	var tiers [][]string
	for i := range numTrackers {
		tiers = append(tiers, []string{fmt.Sprintf("%s/%d/announce", srv.URL, i)})
	}
	_, _, err = cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{InfoHash: metainfo.HashBytes([]byte("batched announce results"))},
		Trackers:       tiers,
	})
	qt.Assert(t, qt.IsNil(err))

	// Doesn't fail the test itself: the caller may hold the Client lock, which Close needs.
	waitFor := func(cond func() bool) bool {
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}
	if !waitFor(func() bool { return arrived.Load() == numTrackers }) {
		t.Fatalf("%v of %v trackers were asked", arrived.Load(), numTrackers)
	}

	d := &cl.regularTrackerAnnounceDispatcher
	queued := func() (n int, draining bool) {
		d.resultsMu.Lock()
		defer d.resultsMu.Unlock()
		return len(d.results), d.resultsDraining
	}
	// With the Client lock held, every finished announce queues its result, and only the first
	// goes on to wait for the lock to apply them all.
	cl.lock()
	close(release)
	ok := waitFor(func() bool {
		n, draining := queued()
		return draining && n == numTrackers
	})
	n, draining := queued()
	cl.unlock()
	qt.Assert(t, qt.IsTrue(ok), qt.Commentf("queued %v results, draining %v", n, draining))

	if !waitFor(func() bool {
		n, draining := queued()
		return n == 0 && !draining
	}) {
		n, draining := queued()
		t.Fatalf("results not applied: %v queued, draining %v", n, draining)
	}
	cl.lock()
	completed := 0
	var errs []error
	for _, state := range d.announceStates {
		if !state.lastAttemptCompleted.IsZero() {
			completed++
			if state.Err != nil {
				errs = append(errs, state.Err)
			}
		}
	}
	cl.unlock()
	qt.Check(t, qt.Equals(completed, numTrackers))
	qt.Check(t, qt.HasLen(errs, 0))
}
