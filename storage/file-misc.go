package storage

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/anacrolix/torrent/segments"
)

// Returns the minimum file lengths required for the given extent to exist on disk. Returns false if
// the extent is not covered by the files in the index.
func minFileLengthsForTorrentExtent(
	fileSegmentsIndex segments.Index,
	off, n int64,
	each func(fileIndex int, length int64) bool,
) bool {
	return fileSegmentsIndex.Locate(segments.Extent{
		Start:  off,
		Length: n,
	}, func(fileIndex int, segmentBounds segments.Extent) bool {
		return each(fileIndex, segmentBounds.Start+segmentBounds.Length)
	})
}

func fsync(filePath string) (err error) {
	f, err := os.OpenFile(filePath, os.O_WRONLY, filePerm)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// A helper to create zero-length files which won't appear for file-orientated storage since no
// writes will ever occur to them (no torrent data is associated with a zero-length file). The
// caller should make sure the file name provided is safe/sanitized.
func CreateNativeZeroLengthFile(name string) error {
	os.MkdirAll(filepath.Dir(name), dirPerm)
	var f io.Closer
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	return f.Close()
}

type file struct {
	// This protects high level OS file state like partial file name, permission mod, renaming etc.
	mu sync.RWMutex
	// The safe, OS-local file path.
	safeOsPath      string
	beginPieceIndex int
	endPieceIndex   int
	length          int64
	// Utility value to help the race detector find issues for us.
	race byte
}

func (f *file) partFilePath() string {
	return f.safeOsPath + ".part"
}
