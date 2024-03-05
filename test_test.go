package torrent

// Helpers for testing

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func newTestingClient(t testing.TB) *Client {
	cl := new(Client)
	cl.init(TestingConfig(t))
	t.Cleanup(func() {
		cl.Close()
	})
	cl.initLogger()
	return cl
}

func (cl *Client) newTorrentForTesting() *Torrent {
	return cl.newTorrent(metainfo.Hash{1}, nil)
}
