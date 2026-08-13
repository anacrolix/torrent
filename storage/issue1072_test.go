package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

const (
	issue1072PieceLength = 1 << 14
	issue1072NumPieces   = 4
	issue1072NumComplete = issue1072NumPieces / 2
)

func issue1072Info() *metainfo.Info {
	return &metainfo.Info{
		Name:        "a",
		Length:      issue1072PieceLength * issue1072NumPieces,
		PieceLength: issue1072PieceLength,
		Pieces:      make([]byte, 20*issue1072NumPieces),
	}
}

func issue1072PieceData(index int) []byte {
	return bytes.Repeat([]byte{byte('a' + index)}, issue1072PieceLength)
}

// Opens file storage over baseDir with a persistent piece completion, runs f, then closes
// everything down the way a program restart would.
func issue1072WithStorage(
	t *testing.T,
	baseDir string,
	usePartFiles g.Option[bool],
	f func(ti storage.TorrentImpl),
) {
	pc, err := storage.NewDefaultPieceCompletionForDir(baseDir)
	qt.Assert(t, qt.IsNil(err))
	ci := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   baseDir,
		PieceCompletion: pc,
		UsePartFiles:    usePartFiles,
	})
	defer func() { qt.Check(t, qt.IsNil(ci.Close())) }()
	ti, err := ci.OpenTorrent(context.Background(), issue1072Info(), metainfo.HashBytes([]byte("issue1072")))
	qt.Assert(t, qt.IsNil(err))
	defer func() { qt.Check(t, qt.IsNil(ti.Close())) }()
	f(ti)
}

// Writes and marks complete the first half of the torrent's pieces.
func issue1072DownloadHalf(t *testing.T, baseDir string, usePartFiles g.Option[bool]) {
	issue1072WithStorage(t, baseDir, usePartFiles, func(ti storage.TorrentImpl) {
		info := issue1072Info()
		for i := range issue1072NumComplete {
			p := ti.Piece(info.Piece(i))
			_, err := p.WriteAt(issue1072PieceData(i), 0)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.IsNil(p.MarkComplete()))
			qt.Check(t, qt.IsTrue(p.Completion().Complete))
		}
	})
}

// https://github.com/anacrolix/torrent/issues/1072. A partially downloaded torrent, using part
// files and a persistent piece completion, lost all its persisted completion when it was reopened.
// setCompletionFromPartFiles only stats the promoted (non-part) file name, so an incomplete file
// looks like it doesn't exist. It used to force-set every piece the file covers to not complete,
// discarding the data on disk. Now it sets unknown instead, so the client verifies what's there.
func TestIssue1072PartFileCompletionNotWipedOnReopen(t *testing.T) {
	baseDir := t.TempDir()
	issue1072DownloadHalf(t, baseDir, g.Some(true))

	// The file is incomplete, so it hasn't been promoted to its final name. That's the condition
	// that trips the bug.
	_, err := os.Stat(filepath.Join(baseDir, "a.part"))
	qt.Assert(t, qt.IsNil(err))

	issue1072WithStorage(t, baseDir, g.Some(true), func(ti storage.TorrentImpl) {
		info := issue1072Info()
		for i := range issue1072NumPieces {
			c := ti.Piece(info.Piece(i)).Completion()
			qt.Check(t, qt.IsNil(c.Err))
			// Unknown, not "known to be incomplete". Ok true with Complete false would tell the
			// client the data is gone, and it would download the piece again.
			qt.Check(t, qt.IsFalse(c.Ok), qt.Commentf("piece %v completion after restart", i))
		}
		// The data the completion used to be discarded for is still there, and still readable
		// through the part file, so hashing it will restore the completion.
		for i := range issue1072NumComplete {
			var buf bytes.Buffer
			p := info.Piece(i)
			n, err := io.Copy(&buf, io.NewSectionReader(ti.Piece(p), 0, p.Length()))
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(n, p.Length()))
			qt.Check(t, qt.DeepEquals(buf.Bytes(), issue1072PieceData(i)))
		}
	})
}

// Without part files there's no completion reset at all, so completion is simply preserved. The
// control for the case above.
func TestPartialCompletionReopenedWithoutPartFiles(t *testing.T) {
	baseDir := t.TempDir()
	issue1072DownloadHalf(t, baseDir, g.Some(false))
	issue1072WithStorage(t, baseDir, g.Some(false), func(ti storage.TorrentImpl) {
		info := issue1072Info()
		for i := range issue1072NumPieces {
			c := ti.Piece(info.Piece(i)).Completion()
			qt.Check(t, qt.IsNil(c.Err))
			qt.Check(
				t,
				qt.Equals(c.Complete, i < issue1072NumComplete),
				qt.Commentf("piece %v completion after restart", i))
		}
	})
}
