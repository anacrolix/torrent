package storage

import (
	"bytes"
	"context"
	"fmt"
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

// Pieces spanning an empty file in the middle of a torrent are written and read like any other.
func TestPiecesSpanningEmptyFile(t *testing.T) {
	for _, disableMmap := range []bool{false, true} {
		t.Run(fmt.Sprintf("DisableMmap=%v", disableMmap), func(t *testing.T) {
			info := &metainfo.Info{
				Name:        "t",
				PieceLength: 4,
				Pieces:      make([]byte, 20*4),
				Files: []metainfo.FileInfo{
					{Path: []string{"a"}, Length: 6},
					{Path: []string{"empty"}, Length: 0},
					{Path: []string{"b"}, Length: 10},
				},
			}
			dir := t.TempDir()
			s := NewFileOpts(NewFileClientOpts{
				ClientBaseDir:   dir,
				PieceCompletion: NewMapPieceCompletion(),
				DisableMmap:     disableMmap,
			})
			defer s.Close()
			tor, err := s.OpenTorrent(context.Background(), info, metainfo.Hash{})
			qt.Assert(t, qt.IsNil(err))
			defer tor.Close()
			data := []byte("abcdefghijklmnop")
			for i := range 4 {
				p := tor.Piece(info.Piece(i))
				_, err := p.WriteAt(data[i*4:i*4+4], 0)
				qt.Assert(t, qt.IsNil(err), qt.Commentf("piece %v", i))
				qt.Assert(t, qt.IsNil(p.MarkComplete()))
			}
			for i := range 4 {
				p := tor.Piece(info.Piece(i))
				b := make([]byte, 4)
				_, err := p.ReadAt(b, 0)
				qt.Check(t, qt.IsNil(err), qt.Commentf("piece %v", i))
				qt.Check(t, qt.Equals(string(b), string(data[i*4:i*4+4])))
				var buf bytes.Buffer
				_, err = p.(io.WriterTo).WriteTo(&buf)
				qt.Check(t, qt.IsNil(err))
				qt.Check(t, qt.Equals(buf.String(), string(data[i*4:i*4+4])))
			}
			// The empty file is there, and no part file was made for it.
			fi, err := os.Stat(filepath.Join(dir, "t", "empty"))
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(fi.Size(), int64(0)))
			_, err = os.Stat(filepath.Join(dir, "t", "empty.part"))
			qt.Check(t, qt.ErrorIs(err, os.ErrNotExist))
		})
	}
}
