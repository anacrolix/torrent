package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/metainfo"
)

func TestShortFile(t *testing.T) {
	td := t.TempDir()
	s := NewFile(td)
	info := &metainfo.Info{
		Name:        "a",
		Length:      2,
		PieceLength: bytesx.MiB,
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(infoHashPathMaker(td, metainfo.Hash{}, info, nil)), 0700))
	f, err := os.Create(infoHashPathMaker(td, metainfo.Hash{}, info, nil))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(1))
	f.Close()

	ts, err := s.OpenTorrent(info, metainfo.Hash{})
	assert.NoError(t, err)

	var buf bytes.Buffer
	p := info.Piece(0)

	n, err := io.Copy(&buf, io.NewSectionReader(ts, p.Offset(), p.Length()))
	require.Equal(t, io.ErrUnexpectedEOF, err)
	require.Equal(t, int64(1), n)
}
