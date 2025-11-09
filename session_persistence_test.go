package torrent

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	qt "github.com/frankban/quicktest"
	"os"
)

func randomInfoHash() metainfo.Hash {
	var ih metainfo.Hash
	now := time.Now().UnixNano()
	for i := range ih {
		ih[i] = byte(now >> (uint(i) * 3))
	}
	return ih
}

func TestPersistPeersAcrossSessions(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = true
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.DisableWebtorrent = true
	cfg.AcceptPeerConnections = false
	cl, err := NewClient(cfg)
	qt.Assert(t, err, qt.IsNil)
	defer cl.Close()

	ih := randomInfoHash()
	to, _ := cl.AddTorrentInfoHash(ih)
	// Inject a peer.
	to.AddPeers([]PeerInfo{{
		Addr: ipPortAddr{
			IP:   net.IPv4(127, 0, 0, 1),
			Port: 51413,
		},
	}})
	// Closing the client persists peers.
	cl.Close()

	// Recreate client with same DataDir.
	cfg2 := TestingConfig(t)
	cfg2.DataDir = cfg.DataDir
	cfg2.DisableTrackers = true
	cfg2.DisableTCP = true
	cfg2.DisableUTP = true
	cfg2.DisableWebtorrent = true
	cfg2.AcceptPeerConnections = false
	cl2, err := NewClient(cfg2)
	qt.Assert(t, err, qt.IsNil)
	defer cl2.Close()

	to2, _ := cl2.AddTorrentInfoHash(ih)
	// Allow internal initialization.
	time.Sleep(50 * time.Millisecond)
	stats := to2.Stats()
	qt.Assert(t, stats.PendingPeers, qt.Not(qt.Equals), 0)
}

func TestTrackerIntervalRetainedBetweenSessions(t *testing.T) {
	cfg := TestingConfig(t)
	// Trackers must be enabled for this test.
	cfg.DisableTrackers = false
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.DisableWebtorrent = true
	cfg.AcceptPeerConnections = false

	// Prepare a fake HTTP tracker that counts requests.
	var reqCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		type resp struct {
			FailureReason string `bencode:"failure reason,omitempty"`
			Interval      int32  `bencode:"interval"`
			Complete      int32  `bencode:"complete"`
			Incomplete    int32  `bencode:"incomplete"`
		}
		// Return a small interval; we only assert "no announce yet", not eventual behavior.
		_ = bencode.NewEncoder(w).Encode(resp{Interval: 1})
	}))
	defer ts.Close()
	trURL := ts.URL + "/announce"

	// Seed a session file that says we just announced and should wait 1 hour.
	ih := randomInfoHash()
	sessionPath := filepath.Join(cfg.DataDir, ".session", ih.HexString()+".json")
	err := os.MkdirAll(filepath.Dir(sessionPath), 0o750)
	qt.Assert(t, err, qt.IsNil)
	type persistedTrackerState struct {
		URL             string `json:"url"`
		IntervalSeconds int64  `json:"interval_seconds"`
		CompletedUnix   int64  `json:"completed_unix"`
	}
	payload := struct {
		Trackers map[string]persistedTrackerState `json:"trackers"`
	}{
		Trackers: map[string]persistedTrackerState{
			trURL: {
				URL:             trURL,
				IntervalSeconds: int64(time.Hour / time.Second),
				CompletedUnix:   time.Now().Unix(),
			},
		},
	}
	f, err := os.Create(sessionPath)
	qt.Assert(t, err, qt.IsNil)
	err = json.NewEncoder(f).Encode(payload)
	qt.Assert(t, err, qt.IsNil)
	f.Close()

	cl, err := NewClient(cfg)
	qt.Assert(t, err, qt.IsNil)
	defer cl.Close()
	to, _ := cl.AddTorrentInfoHash(ih)
	to.AddTrackers([][]string{{trURL}})

	// Give the trackerScraper goroutine time to potentially announce if it wasn't retained.
	time.Sleep(200 * time.Millisecond)
	qt.Assert(t, atomic.LoadInt32(&reqCount), qt.Equals, int32(0))
}
