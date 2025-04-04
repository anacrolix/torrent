package tracker

import (
	"testing"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/testx"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedTrackerScheme(t *testing.T) {
	ctx, done := testx.Context(t)
	defer done()
	t.Parallel()
	_, err := Announce{TrackerUrl: "lol://tracker.openbittorrent.com:80/announce"}.Do(ctx, NewAccounceRequest(int160.Random(), 0, int160.Random()))
	require.Equal(t, ErrBadScheme, err)
}
