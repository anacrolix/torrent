package torrent_test

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/james-lawrence/torrent/internal/testx"
	"github.com/james-lawrence/torrent/storage"
)

func TestDropTorrentWithMmapStorageWhileHashing(t *testing.T) {
	ctx, done := testx.Context(t)
	defer done()

	cfg := torrent.TestingConfig(t)
	// Ensure the data is present when the torrent is added, and not obtained
	// over the network as the test runs.
	cfg.DownloadRateLimiter = rate.NewLimiter(0, 0)
	cl, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	td := t.TempDir()
	mi := testutil.GreetingTestTorrent(td)
	ts, err := torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewMMap(td)))
	require.NoError(t, err)
	tt, new, err := cl.Start(ts)
	require.NoError(t, err)
	require.True(t, new)

	go func() {
		time.Sleep(5 * time.Millisecond)
		cl.Stop(ts)
	}()
	_, err = torrent.DownloadInto(ctx, io.Discard, tt, torrent.TuneVerifyFull)
	require.NoError(t, err)
}
