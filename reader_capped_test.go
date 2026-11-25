package torrent

import (
	"context"
	"os"
	"runtime/debug"
	"testing"

	"github.com/anacrolix/missinggo/v2/filecache"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

// A read from a torrent with capped storage must return a wait error (here the reader's cancelled
// context) instead of retrying it. readAt retried every failure once storage had a capacity, by
// recursing, so a read that was waiting for data when its context ended recursed until the stack
// overflowed.
func TestReaderCappedStorageReturnsWaitError(t *testing.T) {
	// Fail fast instead of growing a 1 GB stack if the recursion comes back.
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	cfg := TestingConfig(t)
	capacity := func() (int64, bool) { return 1 << 20, true }
	fc, err := filecache.NewCache(t.TempDir())
	qt.Assert(t, qt.IsNil(err))
	cfg.DefaultStorage = storage.NewResourcePiecesOpts(
		fc.AsResourceProvider(), storage.ResourcePiecesOpts{Capacity: &capacity})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	qt.Assert(t, qt.IsTrue(tt.hasStorageCap()))
	r := tt.NewReader()
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := r.ReadContext(ctx, make([]byte, 1))
	qt.Check(t, qt.Equals(n, 0))
	qt.Check(t, qt.ErrorIs(err, context.Canceled))
}
