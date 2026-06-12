//go:build !windows

package torrentfs

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestIsSubPath(t *testing.T) {
	for _, case_ := range []struct {
		parent, child string
		is            bool
	}{
		{"", "", false},
		{"", "/", true},
		{"", "a", true},
		{"a/b", "a/bc", false},
		{"a/b", "a/b", false},
		{"a/b", "a/b/c", true},
		{"a/b", "a//b", false},
	} {
		qt.Check(t, qt.Equals(IsSubPath(case_.parent, case_.child), case_.is))
	}
}
