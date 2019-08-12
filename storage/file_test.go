package storage

import (
	"bytes"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShortFile(t *testing.T) {
	td, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(td)
	s := NewFile(td)
	info := &metainfo.Info{
		Name:        "a",
		Length:      2,
		PieceLength: missinggo.MiB,
	}
	ts, err := s.OpenTorrent(info, metainfo.Hash{})
	assert.NoError(t, err)
	f, err := os.Create(filepath.Join(td, "a"))
	require.NoError(t, err)
	err = f.Truncate(1)
	require.NoError(t, err)
	f.Close()
	var buf bytes.Buffer
	p := info.Piece(0)
	n, err := io.Copy(&buf, io.NewSectionReader(ts.Piece(p), 0, p.Length()))
	assert.EqualValues(t, 1, n)
	assert.Equal(t, io.ErrUnexpectedEOF, err)
}
