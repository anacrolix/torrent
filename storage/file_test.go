package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/missinggo/v2"
	"github.com/go-quicktest/qt"

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
	qt.Assert(t, qt.IsNil(err))
	f, err := os.Create(filepath.Join(td, "a"))
	qt.Assert(t, qt.IsNil(err))
	err = f.Truncate(1)
	qt.Assert(t, qt.IsNil(err))
	f.Close()
	var buf bytes.Buffer
	p := info.Piece(0)
	n, err := io.Copy(&buf, io.NewSectionReader(ts.Piece(p), 0, p.Length()))
	qt.Check(t, qt.Equals(n, int64(1)))
	switch err {
	case nil, io.EOF:
	default:
		t.Fatalf("unexpected error: %v", err)
	}
}
