package torrent

import (
	"context"
	"io"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestReaderReadContext(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	defer tt.Drop()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Millisecond))
	defer cancel()
	r := tt.Files()[0].NewReader()
	defer r.Close()
	_, err = r.ReadContext(ctx, make([]byte, 1))
	qt.Assert(t, qt.Equals(err, context.DeadlineExceeded))
}

func TestReaderSetContextAndRead(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	defer tt.Drop()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Millisecond))
	defer cancel()
	r := tt.Files()[0].NewReader()
	defer r.Close()
	r.SetContext(ctx)
	_, err = r.Read(make([]byte, 1))
	qt.Assert(t, qt.Equals(err, context.DeadlineExceeded))
}

// File storage that reports a capacity, so the torrent is treated as having capped storage.
type cappedTestStorage struct {
	storage.ClientImpl
	capacity func() (int64, bool)
}

func (me cappedTestStorage) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	ti, err := me.ClientImpl.OpenTorrent(ctx, info, infoHash)
	ti.Capacity = &me.capacity
	return ti, err
}

// Reading from a torrent with storage capacity that has been dropped must fail rather than retry
// (recursing) forever.
func TestReaderReadAfterDropWithStorageCap(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DefaultStorage = cappedTestStorage{
		ClientImpl: storage.NewFileOpts(storage.NewFileClientOpts{
			ClientBaseDir:   t.TempDir(),
			PieceCompletion: storage.NewMapPieceCompletion(),
		}),
		capacity: func() (int64, bool) { return 1 << 20, true },
	}
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(tt.hasStorageCap()))
	r := tt.Files()[0].NewReader()
	defer r.Close()
	tt.Drop()
	errCh := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 1))
		errCh <- err
	}()
	select {
	case err := <-errCh:
		qt.Assert(t, qt.IsNotNil(err))
		qt.Check(t, qt.Not(qt.Equals(err, io.EOF)))
	case <-time.After(5 * time.Second):
		t.Fatal("read from dropped torrent with storage capacity did not return")
	}
}
