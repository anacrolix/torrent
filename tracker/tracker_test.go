package tracker

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestUnsupportedTrackerScheme(t *testing.T) {
	t.Parallel()
	_, err := Announce{TrackerUrl: "lol://tracker.openbittorrent.com:80/announce"}.Do()
	qt.Assert(t, qt.ErrorIs(err, ErrBadScheme))
}
