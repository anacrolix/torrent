package tracker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnsupportedTrackerScheme(t *testing.T) {
	t.Parallel()
	_, err := Announce{TrackerUrl: "lol://tracker.openbittorrent.com:80/announce"}.Do()
	require.Equal(t, ErrBadScheme, err)
}
