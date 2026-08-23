package torrent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// TrackerDialContext must be used when connecting to HTTP trackers. Regression
// test for #1038, where the client-trackers refactor was reported as dropping
// it.
func TestTrackerDialContextUsedForHttpAnnounce(t *testing.T) {
	trackerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/announce" {
			http.NotFound(w, r)
			return
		}
		// Minimal compact-response announce: one peer 127.0.0.1:8080.
		w.Write([]byte("d8:completei1e10:incompletei1e8:intervali60e5:peers6:\x7f\x00\x00\x01\x1f\x90e"))
	}))
	defer trackerSrv.Close()

	var mu sync.Mutex
	var dialedAddrs []string
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.TrackerDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialedAddrs = append(dialedAddrs, network+" "+addr)
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })

	_, new_, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash: metainfo.HashBytes([]byte("issue-1038-probe")),
		},
		Trackers: [][]string{{trackerSrv.URL + "/announce"}},
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(new_))

	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(dialedAddrs)
		mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("TrackerDialContext never invoked; dialed=%v", dialedAddrs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
