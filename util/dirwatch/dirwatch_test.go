package dirwatch

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestDirwatch(t *testing.T) {
	tempDirName := t.TempDir()
	t.Logf("tempdir: %q", tempDirName)
	dw, err := New(tempDirName)
	qt.Assert(t, qt.IsNil(err))
	defer dw.Close()
}
