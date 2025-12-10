package mytimer

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestNowAfterZero(t *testing.T) {
	qt.Check(t, qt.IsTrue(time.Now().After(time.Time{})))
}
