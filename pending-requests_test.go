package torrent

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp"
)

// Ensure that cmp.Diff will detect errors as required.
func TestPendingRequestsDiff(t *testing.T) {
	var a, b pendingRequests
	c := qt.New(t)
	diff := func() string { return cmp.Diff(a.m, b.m) }
	c.Check(diff(), qt.ContentEquals, "")
	a.m = []int{1, 3}
	b.m = []int{1, 2, 3}
	c.Check(diff(), qt.Not(qt.Equals), "")
}
