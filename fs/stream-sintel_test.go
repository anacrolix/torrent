//go:build !windows

package torrentfs_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/fuse"
	fusefs "github.com/anacrolix/fuse/fs"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/go-quicktest/qt"
	"golang.org/x/sync/errgroup"

	"github.com/anacrolix/torrent"
	torrentfs "github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
)

func copyFile(src, dst string) (err error) {
	from, err := os.Open(src)
	if err != nil {
		return
	}
	defer from.Close()
	to, err := os.Create(dst)
	if err != nil {
		return
	}
	defer to.Close()
	_, err = io.Copy(to, from)
	if err != nil {
		return
	}
	return to.Close()
}

func TestStreamSintelMagnet(t *testing.T) {
	ctx := t.Context()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	fileHashes := map[string]string{
		"poster.jpg": "f9223791908131c505d7bdafa7a8aaf5",
		"Sintel.mp4": "083e808d56aa7b146f513b3458658292",
	}
	targetFile := "Sintel.mp4"
	if testing.Short() {
		targetFile = "poster.jpg"
	}
	dir := t.TempDir()
	t.Logf("temp dir: %v", dir)
	metainfoDir := filepath.Join(dir, "torrents")
	mountDir := filepath.Join(dir, "mnt")
	sourceTorrentDir := `../testdata`
	dummyTorrent := filepath.Join(sourceTorrentDir, `debian-10.8.0-amd64-netinst.iso.torrent`)
	t.Log(os.Getwd())
	dummyTorrentDst := filepath.Join(metainfoDir, filepath.Base(dummyTorrent))
	err := os.MkdirAll(filepath.Dir(dummyTorrentDst), 0o700)
	panicif.Err(err)
	err = copyFile(dummyTorrent, dummyTorrentDst)
	panicif.Err(err)
	mi, err := metainfo.LoadFromFile(filepath.Join(sourceTorrentDir, `sintel.torrent`))
	panicif.Err(err)
	m, err := mi.MagnetV2()
	panicif.Err(err)
	err = os.WriteFile(filepath.Join(metainfoDir, "sintel.magnet"), []byte(m.String()), 0600)
	panicif.Err(err)
	cfg := torrent.NewDefaultClientConfig()
	//cfg.Debug = true
	cfg.ListenPort = 0
	cl, err := torrent.NewClient(cfg)
	panicif.Err(err)
	testutil.ExportStatusWriter(cl, "", t)
	defer cl.Close()

	err = os.Mkdir(mountDir, 0700)
	panicif.Err(err)
	conn, err := fuse.Mount(mountDir)
	panicif.Err(err)
	t.Cleanup(func() { fuse.Unmount(mountDir) })
	t.Cleanup(func() { conn.Close() })
	fs := torrentfs.New(cl)
	var eg errgroup.Group
	eg.Go(func() (err error) {
		err = fusefs.Serve(conn, fs)
		if err != nil {
			err = fmt.Errorf("serving fuse: %w", err)
			t.Log(err)
			return
		}
		return
	})
	<-conn.Ready
	err = conn.MountError
	if err != nil {
		err = fmt.Errorf("conn mount error: %w", err)
	}

	go func() {
		_, err := cl.AddTorrent(mi)
		panicif.Err(err)
		_, err = cl.AddMagnet(m.String())
		panicif.Err(err)
	}()

	f, err := openFileWhenExists(t, filepath.Join(mountDir, "Sintel", targetFile))
	panicif.Err(err)
	t.Logf("opened %v", f.Name())
	t.Cleanup(func() { f.Close() })

	fi, err := f.Stat()
	panicif.Err(err)

	var written int64
	w := writer{
		onWrite: func(p []byte) (n int, err error) {
			written += int64(len(p))
			t.Logf("wrote %v bytes", len(p))
			t.Logf("progress %v", float64(written)/float64(fi.Size()))
			return len(p), nil
		},
	}
	h := md5.New()
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	_, err = f.WriteTo(io.MultiWriter(h, &w))
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	panicif.Err(err)
	err = f.Close()
	panicif.Err(err)

	qt.Assert(t, qt.Equals(hex.EncodeToString(h.Sum(nil)), fileHashes[targetFile]))

	err = fuse.Unmount(mountDir)
	panicif.Err(err)
	err = eg.Wait()
	panicif.Err(err)
}

func openFileWhenExists(t *testing.T, name string) (f *os.File, err error) {
	ctx := t.Context()
	for {
		f, err = os.Open(name)
		if err == nil {
			return
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return
		}
		t.Logf("file does not yet exist: %v", name)
		select {
		case <-ctx.Done():
			err = context.Cause(ctx)
			return
		case <-time.After(1 * time.Second):
		}
	}
}

type writer struct {
	onWrite func(b []byte) (n int, err error)
}

func (w writer) Write(p []byte) (n int, err error) {
	return w.onWrite(p)
}
