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
	defer ts.Close()
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

// The memory mapped implementation faults when a mapped file is replaced or
// truncated by something else, so clients that share their files with another
// writer need a way to ask for the classic one.
func TestDisableMmapSelectsTheClassicFileIo(t *testing.T) {
	if _, ok := (NewFileClientOpts{DisableMmap: true}).fileIo().(*classicFileIo); !ok {
		t.Fatalf("DisableMmap gives %T, want the classic file io", (NewFileClientOpts{DisableMmap: true}).fileIo())
	}
	def := NewFileClientOpts{}.fileIo()
	if _, ok := def.(*classicFileIo); ok != (os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO") == "classic") {
		t.Fatalf("default file io is %T, which does not follow TORRENT_STORAGE_DEFAULT_FILE_IO=%q",
			def, os.Getenv("TORRENT_STORAGE_DEFAULT_FILE_IO"))
	}
}
