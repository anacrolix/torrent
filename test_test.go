package torrent

// Helpers for testing

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func newTestingClient(t testing.TB) *Client {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() {
		cl.Close()
	})
	return cl
}

func (cl *Client) newTorrentForTesting() *Torrent {
	return cl.newTorrent(metainfo.Hash{1}, nil)
}
