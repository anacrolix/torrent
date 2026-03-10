//go:build !windows

// Package tfstest provides a shared test suite for torrentfs Backend
// implementations (e.g. hanwen-torrentfs, og-torrentfs).
package tfstest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent"
	torrentfs "github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// MountFunc mounts tfs at mountDir and returns a cleanup/unmount function.
// If FUSE is unavailable the implementation should call t.Skip.
type MountFunc func(t testing.TB, tfs *torrentfs.TorrentFS, mountDir string) (unmount func())

// startDeadlineBreaker starts a goroutine that calls tfs.Destroy() and
// unmount() shortly before the test deadline, preventing the test from
// hanging indefinitely in FUSE operations when it times out. If the test
// has no deadline, this is a no-op.
func startDeadlineBreaker(t *testing.T, tfs *torrentfs.TorrentFS, unmount func()) {
	deadline, ok := t.Deadline()
	if !ok {
		return
	}
	wait := time.Until(deadline) - 5*time.Second
	if wait <= 0 {
		return
	}
	timer := time.AfterFunc(wait, func() {
		tfs.Destroy()
		unmount()
	})
	t.Cleanup(func() { timer.Stop() })
}

// RunTestSuite runs the full torrentfs integration test suite using the
// provided mount function. Call it from a Test* function in the backend repo:
//
//	func TestTorrentFS(t *testing.T) {
//	    torrentfstest.RunTestSuite(t, myMountFunc)
//	}
func RunTestSuite(t *testing.T, mount MountFunc) {
	t.Run("UnmountWedged", func(t *testing.T) { testUnmountWedged(t, mount) })
	t.Run("DownloadOnDemand", func(t *testing.T) { testDownloadOnDemand(t, mount) })
	t.Run("StreamSintel", func(t *testing.T) { testStreamSintel(t, mount) })
}

// layout holds temporary directories for a test.
type layout struct {
	BaseDir   string
	MountDir  string
	Completed string
	Metainfo  *metainfo.MetaInfo
}

func (tl *layout) destroy() error {
	return os.RemoveAll(tl.BaseDir)
}

func newGreetingLayout(t testing.TB) (tl layout) {
	tl.BaseDir = t.TempDir()
	tl.Completed = filepath.Join(tl.BaseDir, "completed")
	os.Mkdir(tl.Completed, 0o777)
	tl.MountDir = filepath.Join(tl.BaseDir, "mnt")
	os.Mkdir(tl.MountDir, 0o777)
	testutil.CreateDummyTorrentData(tl.Completed)
	tl.Metainfo = testutil.GreetingMetaInfo()
	return
}

// testUnmountWedged verifies that a blocked read is interrupted cleanly when
// Destroy is called before the filesystem is unmounted.
func testUnmountWedged(t *testing.T, mount MountFunc) {
	layout := newGreetingLayout(t)
	defer layout.destroy()

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = filepath.Join(layout.BaseDir, "incomplete")
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	client, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	tt, err := client.AddTorrent(layout.Metainfo)
	require.NoError(t, err)

	tfs := torrentfs.New(client)
	unmount := mount(t, tfs, layout.MountDir)
	// unmount is registered as a cleanup but the test also calls it explicitly
	// after Destroy; the implementation must tolerate double-unmount.
	t.Cleanup(unmount)
	startDeadlineBreaker(t, tfs, unmount)

	ctx, cancel := context.WithCancel(context.Background())
	// readErrCh carries the result of the blocking ReadFile. Using a channel
	// (instead of require inside the goroutine) avoids calling t.FailNow from
	// a non-test goroutine, which panics in Go 1.21+.
	readErrCh := make(chan error, 1)
	go func() {
		defer cancel()
		_, err := os.ReadFile(filepath.Join(layout.MountDir, tt.Info().BestName()))
		readErrCh <- err
	}()

	// Wait until the read has blocked inside the filesystem.
	tfs.WaitBlockedReads(ctx, 1)

	tfs.Destroy()

	// Wait for the read goroutine to finish before unmounting. The goroutine
	// holds an open fd in the mount; unmounting while it's still open leaves
	// the mount in a zombie state with some FUSE backends (e.g. anacrolix/fuse
	// does not fall back to MNT_DETACH). Destroy() above interrupts the blocked
	// read, so ctx will be cancelled promptly. Some backends (e.g. fuse-t via
	// NFS) may not propagate the FUSE error back to the blocking read, so we
	// time out rather than waiting indefinitely.
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
	}
	unmount()

	// Assert that the read returned an error (it must not succeed, since the
	// torrent has no data and Destroy was called). Give 1s for the goroutine
	// to unblock after the mount is gone; if it is still stuck, skip the
	// assertion rather than blocking indefinitely.
	select {
	case readErr := <-readErrCh:
		require.Error(t, readErr)
	case <-time.After(time.Second):
	}
}

// testDownloadOnDemand verifies that reading a file through the FUSE mount
// triggers torrent download from a seeder.
func testDownloadOnDemand(t *testing.T, mount MountFunc) {
	layout := newGreetingLayout(t)
	defer layout.destroy()

	// Seeder: has completed data.
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = layout.Completed
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.Seed = true
	cfg.ListenPort = 0
	cfg.ListenHost = torrent.LoopbackListenHost
	seeder, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s", t)()

	seederTorrent, err := seeder.AddMagnet(
		fmt.Sprintf("magnet:?xt=urn:btih:%s", layout.Metainfo.HashInfoBytes().HexString()),
	)
	require.NoError(t, err)
	go func() {
		<-seederTorrent.GotInfo()
		seederTorrent.VerifyDataContext(context.TODO())
	}()

	// Leecher: no data, connected to seeder.
	cfg = torrent.NewDefaultClientConfig()
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.DisableTCP = true
	cfg.DefaultStorage = storage.NewMMap(filepath.Join(layout.BaseDir, "download"))
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	leecher, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer testutil.ExportStatusWriter(leecher, "l", t)()
	defer leecher.Close()

	leecherTorrent, err := leecher.AddTorrent(layout.Metainfo)
	require.NoError(t, err)
	leecherTorrent.AddClientPeer(seeder)

	tfs := torrentfs.New(leecher)
	defer tfs.Destroy()
	unmount := mount(t, tfs, layout.MountDir)
	t.Cleanup(unmount)
	startDeadlineBreaker(t, tfs, unmount)

	data, err := os.ReadFile(filepath.Join(layout.MountDir, "greeting"))
	require.NoError(t, err)
	assert.EqualValues(t, testutil.GreetingFileContents, data)
}
