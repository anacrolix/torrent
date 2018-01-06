package dirwatch

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDirwatch(t *testing.T) {
	tempDirName, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirName)
	t.Logf("tempdir: %q", tempDirName)
	dw, err := New(tempDirName)
	require.NoError(t, err)
	defer dw.Close()
}
