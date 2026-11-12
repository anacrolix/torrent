package storage

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func openFileStorageForTest(t *testing.T, dir string, info *metainfo.Info, partFiles bool) (TorrentImpl, error) {
	t.Helper()
	cl := NewFileOpts(NewFileClientOpts{
		ClientBaseDir: dir,
		UsePartFiles:  g.Some(partFiles),
	})
	t.Cleanup(func() { cl.Close() })
	ts, err := cl.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err == nil {
		t.Cleanup(func() { ts.Close() })
	}
	return ts, err
}

func writeTestFile(t *testing.T, name, contents string) {
	t.Helper()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Dir(name), 0o750)))
	qt.Assert(t, qt.IsNil(os.WriteFile(name, []byte(contents), 0o640)))
}

func assertFileContents(t *testing.T, name, contents string) {
	t.Helper()
	b, err := os.ReadFile(name)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(string(b), contents))
}

func multiFileInfo(files ...metainfo.FileInfo) *metainfo.Info {
	var total int64
	for _, f := range files {
		total += f.Length
	}
	const pieceLength = 4
	return &metainfo.Info{
		Name:        "t",
		Files:       files,
		PieceLength: pieceLength,
		Pieces:      make([]byte, 20*((total+pieceLength-1)/pieceLength)),
	}
}

// A zero-length entry must not truncate a file that's already at its path.
func TestZeroLengthFileKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "t", "empty")
	writeTestFile(t, existing, "keep me")
	info := multiFileInfo(
		metainfo.FileInfo{Path: []string{"empty"}, Length: 0},
		metainfo.FileInfo{Path: []string{"new-empty"}, Length: 0},
		metainfo.FileInfo{Path: []string{"data"}, Length: 1},
	)
	_, err := openFileStorageForTest(t, dir, info, true)
	qt.Assert(t, qt.IsNil(err))
	assertFileContents(t, existing, "keep me")
	// Missing zero-length files are still created.
	assertFileContents(t, filepath.Join(dir, "t", "new-empty"), "")
}

func TestOpenTorrentRejectsConflictingFilePaths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     []metainfo.FileInfo
		partFiles bool
	}{
		{
			name: "SamePath",
			files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 5},
				{Path: []string{"a"}, Length: 0},
			},
		},
		{
			name: "SamePathAfterCleaning",
			files: []metainfo.FileInfo{
				{Path: []string{"d", "a"}, Length: 5},
				{Path: []string{"d", ".", "a"}, Length: 2},
			},
		},
		{
			name: "PartFilePath",
			files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 5},
				{Path: []string{"a.part"}, Length: 2},
			},
			partFiles: true,
		},
		{
			name: "FileIsAnotherFilesDir",
			files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 5},
				{Path: []string{"a", "b"}, Length: 2},
			},
		},
		{
			name: "DirIsAnotherFile",
			files: []metainfo.FileInfo{
				{Path: []string{"a", "b"}, Length: 2},
				{Path: []string{"a"}, Length: 0},
			},
		},
		{
			name: "PaddingFilesOfDifferentLengths",
			files: []metainfo.FileInfo{
				{Path: []string{".pad", "x"}, Length: 2, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
				{Path: []string{".pad", "x"}, Length: 3, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
			},
		},
		{
			name: "PaddingAndRegularFile",
			files: []metainfo.FileInfo{
				{Path: []string{".pad", "2"}, Length: 2, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
				{Path: []string{".pad", "2"}, Length: 2},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			existing := filepath.Join(dir, "t", "a")
			writeTestFile(t, existing, "hello")
			_, err := openFileStorageForTest(t, dir, multiFileInfo(tc.files...), tc.partFiles)
			qt.Assert(t, qt.IsNotNil(err))
			assertFileContents(t, existing, "hello")
		})
	}
}

func TestOpenTorrentAllowsNonConflictingFilePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []metainfo.FileInfo
	}{
		{
			// Without part files, a file can be named like another file's part file.
			name: "PartFileNameWithoutPartFiles",
			files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 5},
				{Path: []string{"a.part"}, Length: 2},
			},
		},
		{
			name: "SharedDirectories",
			files: []metainfo.FileInfo{
				{Path: []string{"d", "e", "a"}, Length: 5},
				{Path: []string{"d", "e", "b"}, Length: 2},
				{Path: []string{"d", "c"}, Length: 2},
			},
		},
		{
			// Some torrent creators name padding files by their length.
			name: "PaddingFilesOfSameLength",
			files: []metainfo.FileInfo{
				{Path: []string{"a"}, Length: 2},
				{Path: []string{".pad", "2"}, Length: 2, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
				{Path: []string{"b"}, Length: 2},
				{Path: []string{".pad", "2"}, Length: 2, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openFileStorageForTest(t, t.TempDir(), multiFileInfo(tc.files...), false)
			qt.Assert(t, qt.IsNil(err))
			_, err = openFileStorageForTest(t, t.TempDir(), multiFileInfo(tc.files...), tc.name != "PartFileNameWithoutPartFiles")
			qt.Assert(t, qt.IsNil(err))
		})
	}
}

// A single-file torrent whose name is empty or "." would otherwise be stored at the torrent
// directory itself, with its part file beside it. It's named after the infohash instead.
func TestSingleFileTorrentWithoutName(t *testing.T) {
	for _, name := range []string{"", ".", metainfo.NoName} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "downloads")
			qt.Assert(t, qt.IsNil(os.Mkdir(dir, 0o750)))
			info := &metainfo.Info{
				Name:        name,
				Length:      3,
				PieceLength: 4,
				Pieces:      make([]byte, 20),
			}
			ts, err := openFileStorageForTest(t, dir, info, true)
			qt.Assert(t, qt.IsNil(err))
			_, err = ts.Piece(info.Piece(0)).WriteAt([]byte("abc"), 0)
			qt.Assert(t, qt.IsNil(err))
			entries, err := os.ReadDir(parent)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.HasLen(entries, 1))
			assertFileContents(t, filepath.Join(dir, metainfo.Hash{}.HexString()+partFileSuffix), "abc")
		})
	}
}

// A custom path maker that puts a file at the torrent directory is rejected.
func TestOpenTorrentRejectsFileAtTorrentDir(t *testing.T) {
	dir := t.TempDir()
	cl := NewFileOpts(NewFileClientOpts{
		ClientBaseDir: dir,
		FilePathMaker: func(FilePathMakerOpts) string { return "" },
	})
	defer cl.Close()
	_, err := cl.OpenTorrent(context.Background(), multiFileInfo(metainfo.FileInfo{Path: []string{"a"}, Length: 1}), metainfo.Hash{})
	qt.Assert(t, qt.IsNotNil(err))
}

func TestRenameNoReplace(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rename func(from, to string) error
	}{
		{"Sys", renameNoReplace},
		{"Fallback", renameNoReplaceFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			from := filepath.Join(dir, "from")
			to := filepath.Join(dir, "to")
			writeTestFile(t, from, "new")
			writeTestFile(t, to, "old")
			err := tc.rename(from, to)
			qt.Assert(t, qt.ErrorIs(err, fs.ErrExist))
			assertFileContents(t, from, "new")
			assertFileContents(t, to, "old")
			qt.Assert(t, qt.IsNil(os.Remove(to)))
			qt.Assert(t, qt.IsNil(tc.rename(from, to)))
			assertFileContents(t, to, "new")
			_, err = os.Stat(from)
			qt.Assert(t, qt.ErrorIs(err, fs.ErrNotExist))
			err = tc.rename(from, to)
			qt.Assert(t, qt.ErrorIs(err, fs.ErrNotExist))
		})
	}
}

// Completing a file must not replace a file that's already at its final path.
func TestPromotePartFileKeepsExistingFile(t *testing.T) {
	for _, mmap := range []bool{false, true} {
		t.Run(map[bool]string{false: "Classic", true: "Mmap"}[mmap], func(t *testing.T) {
			dir := t.TempDir()
			final := filepath.Join(dir, "a")
			writeTestFile(t, final, "existing")
			info := &metainfo.Info{
				Name:        "a",
				Length:      4,
				PieceLength: 4,
				Pieces:      make([]byte, 20),
			}
			cl := NewFileOpts(NewFileClientOpts{
				ClientBaseDir: dir,
				DisableMmap:   !mmap,
			})
			defer cl.Close()
			ts, err := cl.OpenTorrent(context.Background(), info, metainfo.Hash{})
			qt.Assert(t, qt.IsNil(err))
			defer ts.Close()
			p := ts.Piece(info.Piece(0))
			_, err = p.WriteAt([]byte("data"), 0)
			qt.Assert(t, qt.IsNil(err))
			err = p.MarkComplete()
			qt.Assert(t, qt.ErrorIs(err, fs.ErrExist))
			assertFileContents(t, final, "existing")
			// The downloaded data is still readable from the part file.
			b := make([]byte, 4)
			n, err := p.ReadAt(b, 0)
			if !errors.Is(err, io.EOF) {
				qt.Assert(t, qt.IsNil(err))
			}
			qt.Assert(t, qt.Equals(string(b[:n]), "data"))
		})
	}
}

// Padding files sharing a path are promoted once, and the others' part files are removed.
func TestPromoteSharedPaddingFile(t *testing.T) {
	dir := t.TempDir()
	pad := metainfo.FileInfo{Path: []string{".pad", "2"}, Length: 2, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}}
	info := multiFileInfo(
		metainfo.FileInfo{Path: []string{"a"}, Length: 2},
		pad,
		metainfo.FileInfo{Path: []string{"b"}, Length: 2},
		pad,
	)
	ts, err := openFileStorageForTest(t, dir, info, true)
	qt.Assert(t, qt.IsNil(err))
	for i := range info.NumPieces() {
		p := ts.Piece(info.Piece(i))
		_, err = p.WriteAt([]byte{byte('a' + i), byte('a' + i), 0, 0}, 0)
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.IsNil(p.MarkComplete()))
	}
	padPath := filepath.Join(dir, "t", ".pad", "2")
	assertFileContents(t, padPath, "\x00\x00")
	_, err = os.Stat(padPath + partFileSuffix)
	qt.Assert(t, qt.ErrorIs(err, fs.ErrNotExist))
	assertFileContents(t, filepath.Join(dir, "t", "a"), "aa")
	assertFileContents(t, filepath.Join(dir, "t", "b"), "bb")
}
