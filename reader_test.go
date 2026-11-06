package torrent

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
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

// SetReadahead may be called on a reader while another goroutine reads from it, such as when a
// streaming server redistributes readahead across its active readers.
func TestReaderSetReadaheadDuringRead(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	cfg := TestingConfig(t)
	cfg.DataDir = dir
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	defer tt.Drop()
	qt.Assert(t, qt.IsNil(tt.VerifyDataContext(t.Context())))

	r := tt.NewReader()
	defer r.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4)
		for range 20000 {
			if _, err := r.Seek(0, io.SeekStart); err != nil {
				t.Error(err)
				return
			}
			if _, err := r.Read(buf); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := range 20000 {
		r.SetReadahead(int64(i))
	}
	<-done
}
