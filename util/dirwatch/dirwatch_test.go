package dirwatch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirwatch(t *testing.T) {
	dw, err := New(t.TempDir())
	require.NoError(t, err)
	defer dw.Close()
}
