//go:build !windows

package tfstest

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent"
	torrentfs "github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/metainfo"
)

// sintelFileHashes are the expected MD5 hashes of files in the Sintel torrent.
var sintelFileHashes = map[string]string{
	"poster.jpg": "f9223791908131c505d7bdafa7a8aaf5",
	"Sintel.mp4": "083e808d56aa7b146f513b3458658292",
}

// testStreamSintel verifies that a large multi-file torrent can be streamed
// through the FUSE mount. It downloads the Sintel open-movie torrent via
// BitTorrent and reads a file through the filesystem, verifying its MD5.
//
// This test requires internet access and is skipped by default because it is
// slow and occasionally flaky.
func testStreamSintel(t *testing.T, mount MountFunc) {
	// Locate testdata relative to this source file so the test works both when
	// run from within the torrent module and when called from a backend repo
	// that replaces github.com/anacrolix/torrent with a local path.
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("runtime.Caller failed: cannot locate testdata")
	}
	testdataDir := filepath.Join(filepath.Dir(filename), "..", "..", "testdata")

	sintelTorrentPath := filepath.Join(testdataDir, "sintel.torrent")
	if _, err := os.Stat(sintelTorrentPath); err != nil {
		t.Skipf("sintel.torrent not found at %v: %v", sintelTorrentPath, err)
	}

	// Quick internet connectivity check: try a TCP connection to a well-known
	// address. If it fails, the torrent cannot be downloaded and the test is
	// pointless – skip early rather than hanging until the test deadline.
	if !hasInternetConnectivity() {
		t.Skip("no internet connectivity: skipping from-scratch Sintel download test")
	}

	ctx := t.Context()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	targetFile := "Sintel.mp4"
	if testing.Short() {
		targetFile = "poster.jpg"
	}

	mi, err := metainfo.LoadFromFile(sintelTorrentPath)
	require.NoError(t, err)
	m, err := mi.MagnetV2()
	require.NoError(t, err)

	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = 0
	cfg.HTTPProxy = http.ProxyFromEnvironment
	cl, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	mountDir := t.TempDir()
	tfs := torrentfs.New(cl)
	defer tfs.Destroy()
	unmount := mount(t, tfs, mountDir)
	t.Cleanup(unmount)
	// When ctx is done (interrupt) or the test deadline approaches, destroy tfs
	// and unmount so that any goroutines blocked in FUSE operations are unblocked.
	go func() {
		cleanupCtx := ctx
		if deadline, ok := t.Deadline(); ok {
			var deadlineCancel context.CancelFunc
			// Leave 15s before the deadline: our unmount path may need up to ~6s
			// (3s timeout + force-unmount + goroutine join) on slow backends like
			// fuse-t, so 5s was not enough.
			cleanupCtx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-15*time.Second))
			defer deadlineCancel()
		}
		<-cleanupCtx.Done()
		tfs.Destroy()
		unmount()
	}()

	go func() {
		_, err := cl.AddTorrent(mi)
		if err != nil {
			t.Logf("AddTorrent: %v", err)
		}
		_, err = cl.AddMagnet(m.String())
		if err != nil {
			t.Logf("AddMagnet: %v", err)
		}
	}()

	f, err := openFileWhenExists(t, ctx, filepath.Join(mountDir, "Sintel", targetFile))
	require.NoError(t, err)
	t.Logf("opened %v", f.Name())
	defer f.Close()

	fi, err := f.Stat()
	require.NoError(t, err)

	var written int64
	h := md5.New()
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	start := time.Now()
	_, err = f.WriteTo(io.MultiWriter(h, &progressWriter{
		onWrite: func(n int) {
			written += int64(n)
			elapsed := time.Since(start).Seconds()
			var rate float64
			if elapsed > 0 {
				rate = float64(written) / elapsed / (1 << 20)
			}
			t.Logf("progress %.2f%% (%.2f MiB/s)", 100*float64(written)/float64(fi.Size()), rate)
		},
	}))
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	require.NoError(t, err)

	require.Equal(t, sintelFileHashes[targetFile], hex.EncodeToString(h.Sum(nil)))

	// Warm re-read: all pieces are now fully local. Re-read via the same FUSE
	// mount without any network activity to verify the filesystem handles a
	// completed torrent correctly.
	f2, err := os.Open(filepath.Join(mountDir, "Sintel", targetFile))
	require.NoError(t, err)
	defer f2.Close()
	h2 := md5.New()
	_, err = io.Copy(h2, f2)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	require.NoError(t, err)
	require.Equal(t, sintelFileHashes[targetFile], hex.EncodeToString(h2.Sum(nil)))
}

func openFileWhenExists(t *testing.T, ctx context.Context, name string) (*os.File, error) {
	for {
		type openResult struct {
			f   *os.File
			err error
		}
		ch := make(chan openResult, 1)
		go func() {
			f, err := os.Open(name)
			ch <- openResult{f, err}
		}()
		var res openResult
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case res = <-ch:
		}
		if res.err == nil {
			return res.f, nil
		}
		if !errors.Is(res.err, fs.ErrNotExist) && !errors.Is(res.err, syscall.ENOTDIR) {
			return nil, res.err
		}
		t.Logf("waiting for file to appear: %v", name)
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(time.Second):
		}
	}
}

// hasInternetConnectivity reports whether outbound internet connectivity seems
// available by attempting a short-timeout TCP dial to a well-known address.
func hasInternetConnectivity() bool {
	conn, err := net.DialTimeout("tcp", "1.1.1.1:443", 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}


type progressWriter struct {
	onWrite func(n int)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.onWrite(len(p))
	return len(p), nil
}
