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

var testingTorrentInfoHash = metainfo.Hash{1}

// Basic opts for testing adding torrents I guess.
var testingAddTorrentOpts = AddTorrentOpts{
	InfoHash:                 testingTorrentInfoHash,
	DisableInitialPieceCheck: true,
}

// Why would you call this? It doesn't add it to the Client...
func (cl *Client) newTorrentForTesting() *Torrent {
	return cl.newTorrentOpt(testingAddTorrentOpts)
}
