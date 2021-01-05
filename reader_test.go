package torrent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/internal/testutil"
)

func TestReaderReadContext(t *testing.T) {
	cl, err := torrent.NewClient(torrent.TestingConfig(t))
	require.NoError(t, err)
	defer cl.Close()
	ts, err := torrent.NewFromMetaInfo(testutil.GreetingMetaInfo())
	require.NoError(t, err)
	tt, _, err := cl.Start(ts)
	require.NoError(t, err)
	defer cl.Stop(ts)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Millisecond))
	defer cancel()
	r := tt.Files()[0].NewReader()
	defer r.Close()
	_, err = r.ReadContext(ctx, make([]byte, 1))
	require.EqualValues(t, context.DeadlineExceeded, err)
}
