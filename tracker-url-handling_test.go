package torrent

import (
	"context"
	"errors"
	"net"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// Tracker URLs that can't be used should be skipped rather than stopping the client, and URLs
// that are merely not in normal form should still be used.
func TestUnusableTrackerUrlsAreSkipped(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.TrackerDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, errors.New("no dialing in this test")
	}
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })

	tor, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash: metainfo.HashBytes([]byte("tracker-url-handling")),
		},
		Trackers: [][]string{{
			"ftp://example.invalid/announce",
			"http://user:pass@example.invalid/announce",
			"announce",
			"HTTP://example.invalid/announce",
		}},
	})
	qt.Assert(t, qt.IsNil(err))

	cl.rLock()
	defer cl.rUnlock()
	var urls []string
	for key := range tor.trackerAnnouncers {
		urls = append(urls, string(key.url))
	}
	qt.Assert(t, qt.DeepEquals(urls, []string{"http://example.invalid/announce"}))
}
