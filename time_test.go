package torrent

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestZeroTimeAfterNow(t *testing.T) {
	var zeroTime time.Time
	qt.Assert(t, qt.IsFalse(zeroTime.After(time.Now())))
	qt.Assert(t, qt.Equals(zeroTime.Compare(zeroTime), 0))
}
