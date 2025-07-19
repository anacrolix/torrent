package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"github.com/anacrolix/missinggo/v2"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type fileTorrentImpl struct {
	info              *metainfo.Info
	files             []fileExtra
	metainfoFileInfos []metainfo.FileInfo
	segmentLocater    segments.Index
	infoHash          metainfo.Hash
	// Save memory by pointing to the other data.
	client *fileClientImpl
}

func (fts *fileTorrentImpl) logger() *slog.Logger {
	return fts.client.opts.Logger
}

func (fts *fileTorrentImpl) pieceCompletion() PieceCompletion {
	return fts.client.opts.PieceCompletion
}

func (fts *fileTorrentImpl) pieceCompletionKey(p int) metainfo.PieceKey {
	return metainfo.PieceKey{
		InfoHash: fts.infoHash,
		Index:    p,
	}
}

func (fts *fileTorrentImpl) setPieceCompletion(p int, complete bool) error {
	return fts.pieceCompletion().Set(fts.pieceCompletionKey(p), complete)
}

// Set piece completions based on whether all files in each piece are not .part files.
func (fts *fileTorrentImpl) setCompletionFromPartFiles() error {
	notComplete := make([]bool, fts.info.NumPieces())
	for fileIndex := range fts.files {
		f := fts.file(fileIndex)
		fi, err := os.Stat(f.safeOsPath)
		if err == nil {
			if fi.Size() == f.length() {
				continue
			}
			fts.logger().Warn("file has unexpected size", "file", f.safeOsPath, "size", fi.Size(), "expected", f.length())
		} else if !errors.Is(err, fs.ErrNotExist) {
			fts.logger().Warn("error checking file size", "err", err)
		}
		for pieceIndex := f.beginPieceIndex(); pieceIndex < f.endPieceIndex(); pieceIndex++ {
			notComplete[pieceIndex] = true
		}
	}
	for i, nc := range notComplete {
		if nc {
			// Use whatever the piece completion has, or trigger a hash.
			continue
		}
		err := fts.setPieceCompletion(i, true)
		if err != nil {
			return fmt.Errorf("setting piece %v completion: %w", i, err)
		}
	}
	return nil
}

func (fts *fileTorrentImpl) partFiles() bool {
	return fts.client.opts.partFiles()
}

func (fts *fileTorrentImpl) pathForWrite(f *file) string {
	if fts.partFiles() {
		return f.partFilePath()
	}
	return f.safeOsPath
}

func (fts *fileTorrentImpl) getCompletion(piece int) Completion {
	cmpl, err := fts.pieceCompletion().Get(metainfo.PieceKey{fts.infoHash, piece})
	cmpl.Err = errors.Join(cmpl.Err, err)
	return cmpl
}

func (fts *fileTorrentImpl) Piece(p metainfo.Piece) PieceImpl {
	// Create a view onto the file-based torrent storage.
	_io := fileTorrentImplIO{fts}
	// Return the appropriate segments of this.
	return &filePieceImpl{
		fts,
		p,
		missinggo.NewSectionWriter(_io, p.Offset(), p.Length()),
		io.NewSectionReader(_io, p.Offset(), p.Length()),
	}
}

func (fs *fileTorrentImpl) Close() error {
	return nil
}

func (fts *fileTorrentImpl) Flush() error {
	for i := range fts.files {
		f := fts.file(i)
		fts.logger().Debug("flushing", "file.safeOsPath", f.safeOsPath)
		if err := fsync(fts.pathForWrite(&f)); err != nil {
			return err
		}
	}
	return nil
}

func (fts *fileTorrentImpl) file(index int) file {
	return file{
		Info:      fts.info,
		FileInfo:  &fts.metainfoFileInfos[index],
		fileExtra: &fts.files[index],
	}
}

// Open file for reading.
func (me *fileTorrentImpl) openSharedFile(file file) (f sharedFileIf, err error) {
	file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	if me.partFiles() {
		f, err = sharedFiles.Open(file.partFilePath())
	}
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = sharedFiles.Open(file.safeOsPath)
	}
	file.mu.RUnlock()
	return
}

// Open file for reading.
func (me *fileTorrentImpl) openFile(file file) (f *os.File, err error) {
	file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	if me.partFiles() {
		f, err = os.Open(file.partFilePath())
	}
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = os.Open(file.safeOsPath)
	}
	file.mu.RUnlock()
	return
}
