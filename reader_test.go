package torrent

import (
	"context"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
	"github.com/stretchr/testify/require"

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
	require.EqualValues(t, context.DeadlineExceeded, err)
}
