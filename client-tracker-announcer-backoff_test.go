package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

func TestFailedAnnounceIntervalDoublesUpToTheCap(t *testing.T) {
	cfg := ClientTrackerConfig{
		FailedAnnounceMinInterval: time.Minute,
		FailedAnnounceMaxInterval: 8 * time.Minute,
	}
	for _, c := range []struct {
		failures int
		want     time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 8 * time.Minute},
		{50, 8 * time.Minute},
	} {
		qt.Check(t, qt.Equals(cfg.failedAnnounceInterval(c.failures), c.want),
			qt.Commentf("after %d consecutive failures", c.failures))
	}
}

func TestFailedAnnounceIntervalDefaults(t *testing.T) {
	var cfg ClientTrackerConfig
	qt.Check(t, qt.Equals(cfg.failedAnnounceInterval(1), defaultFailedAnnounceMinInterval))
	qt.Check(t, qt.Equals(cfg.failedAnnounceInterval(100), defaultFailedAnnounceMaxInterval))
}

// A max below the min is nonsense; the min wins so the interval never shrinks.
func TestFailedAnnounceIntervalWithAMaxBelowTheMin(t *testing.T) {
	cfg := ClientTrackerConfig{
		FailedAnnounceMinInterval: 5 * time.Minute,
		FailedAnnounceMaxInterval: time.Minute,
	}
	qt.Check(t, qt.Equals(cfg.failedAnnounceInterval(1), 5*time.Minute))
	qt.Check(t, qt.Equals(cfg.failedAnnounceInterval(10), 5*time.Minute))
}
