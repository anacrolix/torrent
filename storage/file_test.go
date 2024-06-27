package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/missinggo/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
)

func TestShortFile(t *testing.T) {
	td := t.TempDir()
	s := NewFile(td)
	defer s.Close()
	info := &metainfo.Info{
		Name:        "a",
		Length:      2,
		PieceLength: missinggo.MiB,
		Pieces:      make([]byte, 20),
	}
	ts, err := s.OpenTorrent(context.Background(), info, metainfo.Hash{})
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
	switch err {
	case nil, io.EOF:
	default:
		t.Errorf("expected nil or EOF error from truncated piece, got %v", err)
	}
}
