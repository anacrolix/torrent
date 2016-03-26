package file

import (
	"bytes"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
)

func TestShortFile(t *testing.T) {
	td, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(td)
	data := TorrentData(&metainfo.Info{
		Name:   "a",
		Length: 2,
	}, td)
	f, err := os.Create(filepath.Join(td, "a"))
	err = f.Truncate(1)
	f.Close()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.NewSectionReader(data, 0, 2))
	assert.EqualValues(t, 1, n)
	assert.Equal(t, io.ErrUnexpectedEOF, err)
}
