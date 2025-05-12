package udp

import (
	"math"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestTimeoutMax(t *testing.T) {
	qt.Check(t, qt.Equals(timeout(8), maxTimeout))
	qt.Check(t, qt.Equals(timeout(9), maxTimeout))
	qt.Check(t, qt.Equals(timeout(math.MaxInt32), maxTimeout))
}
