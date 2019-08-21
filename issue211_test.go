package torrent

import (
	"io"
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

func TestDropTorrentWithMmapStorageWhileHashing(t *testing.T) {
	cfg := TestingConfig()
	// Ensure the data is present when the torrent is added, and not obtained
	// over the network as the test runs.
	cfg.DownloadRateLimiter = rate.NewLimiter(0, 0)
	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	td, mi := testutil.GreetingTestTorrent()
	tt, new, err := cl.AddTorrentSpec(&TorrentSpec{
		Storage:   storage.NewMMap(td),
		InfoHash:  mi.HashInfoBytes(),
		InfoBytes: mi.InfoBytes,
	})
	require.NoError(t, err)
	assert.True(t, new)

	r := tt.NewReader()
	go tt.Drop()
	io.Copy(ioutil.Discard, r)
}
