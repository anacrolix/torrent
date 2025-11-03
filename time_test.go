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
	qt.Assert(t, qt.Equals(zeroTime.Compare(time.Now()), -1))
	now := time.Now()
	qt.Assert(t, qt.Equals(zeroTime.Compare(time.Now().Add(1)), -1))
	qt.Assert(t, qt.Equals(now.Compare(time.Now().Add(1)), -1))
}
