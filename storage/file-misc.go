package storage

import (
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

// Returns the minimum file lengths required for the given extent to exist on disk. Returns false if
// the extent is not covered by the files in the index.
func minFileLengthsForTorrentExtent(
	fileSegmentsIndex segments.Index,
	off, n int64,
	each func(fileIndex int, length int64) bool,
) {
	for fileIndex, segmentBounds := range fileSegmentsIndex.LocateIter(segments.Extent{
		Start:  off,
		Length: n,
	}) {
		if !each(fileIndex, segmentBounds.Start+segmentBounds.Length) {
			return
		}
	}
}

func fsync(filePath string) (err error) {
	f, err := os.OpenFile(filePath, os.O_WRONLY, filePerm)
	if err != nil {
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

// Combines data from different locations required to handle files in file storage.
type file struct {
	// Required for piece length.
	*metainfo.Info
	// Enumerated when info is provided.
	*metainfo.FileInfo
	*fileExtra
}

func (f *file) beginPieceIndex() int {
	return f.FileInfo.BeginPieceIndex(f.Info.PieceLength)
}

func (f *file) endPieceIndex() int {
	return f.FileInfo.EndPieceIndex(f.Info.PieceLength)
}

func (f *file) length() int64 {
	return f.FileInfo.Length
}

func (f *file) torrentOffset() int64 {
	return f.FileInfo.TorrentOffset
}

// Extra state in the file storage for each file.
type fileExtra struct {
	// This protects high level OS file state like partial file name, permission mod, renaming etc.
	mu sync.RWMutex
	// The safe, OS-local file path.
	safeOsPath string
	// Utility value to help the race detector find issues for us.
	race byte
}

func (f *fileExtra) partFilePath() string {
	return f.safeOsPath + ".part"
}
