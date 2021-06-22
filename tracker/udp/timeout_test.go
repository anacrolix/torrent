package udp

import (
	"math"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestTimeoutMax(t *testing.T) {
	c := qt.New(t)
	c.Check(timeout(8), qt.Equals, maxTimeout)
	c.Check(timeout(9), qt.Equals, maxTimeout)
	c.Check(timeout(math.MaxInt32), qt.Equals, maxTimeout)
}
