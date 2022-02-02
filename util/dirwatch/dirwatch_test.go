package dirwatch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirwatch(t *testing.T) {
	tempDirName := t.TempDir()
	t.Logf("tempdir: %q", tempDirName)
	dw, err := New(tempDirName)
	require.NoError(t, err)
	defer dw.Close()
}
