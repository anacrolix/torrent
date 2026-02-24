package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type fileTorrentImpl struct {
	info              *metainfo.Info
	files             []fileExtra
	metainfoFileInfos []metainfo.FileInfo
	segmentLocater    segments.Index
	infoHash          metainfo.Hash
	io                fileIo
	// Save memory by pointing to the other data.
	client *fileClientImpl
}

func (me *fileTorrentImpl) logger() *slog.Logger {
	return me.client.opts.Logger
}

func (me *fileTorrentImpl) pieceCompletion() PieceCompletion {
	return me.client.opts.PieceCompletion
}

func (me *fileTorrentImpl) pieceCompletionKey(p int) metainfo.PieceKey {
	return metainfo.PieceKey{
		InfoHash: me.infoHash,
		Index:    p,
	}
}

func (me *fileTorrentImpl) setPieceCompletion(p int, complete bool) error {
	return me.pieceCompletion().Set(me.pieceCompletionKey(p), complete)
}

// Set piece completions based on whether all files in each piece are not .part files.
func (me *fileTorrentImpl) setCompletionFromPartFiles() error {
	notComplete := make([]bool, me.info.NumPieces())
	for fileIndex := range me.files {
		f := me.file(fileIndex)
		fi, err := os.Stat(f.safeOsPath)
		if err == nil {
			if fi.Size() == f.length() {
				continue
			}
			me.logger().Warn("file has unexpected size", "file", f.safeOsPath, "size", fi.Size(), "expected", f.length())
		} else if !errors.Is(err, fs.ErrNotExist) {
			me.logger().Warn("error checking file size", "err", err)
		}
		// Ensure all pieces associated with a file are not marked as complete (at most unknown).
		for pieceIndex := f.beginPieceIndex(); pieceIndex < f.endPieceIndex(); pieceIndex++ {
			notComplete[pieceIndex] = true
		}
	}
	for i, nc := range notComplete {
		if nc {
			c := me.getCompletion(i)
			if c.Complete {
				// TODO: We need to set unknown so that verification of the data we do have could
				// occur naturally but that'll be a big change.
				panicif.Err(me.setPieceCompletion(i, false))
			}
		} else {
			err := me.setPieceCompletion(i, true)
			if err != nil {
				return fmt.Errorf("setting piece %v completion: %w", i, err)
			}
		}
	}
	return nil
}

func (me *fileTorrentImpl) partFiles() bool {
	return me.client.opts.partFiles()
}

func (me *fileTorrentImpl) pathForWrite(f *file) string {
	if me.partFiles() {
		return f.partFilePath()
	}
	return f.safeOsPath
}

func (me *fileTorrentImpl) getCompletion(piece int) Completion {
	cmpl, err := me.pieceCompletion().Get(metainfo.PieceKey{me.infoHash, piece})
	cmpl.Err = errors.Join(cmpl.Err, err)
	return cmpl
}

func (me *fileTorrentImpl) Piece(p metainfo.Piece) PieceImpl {
	// Create a view onto the file-based torrent storage.
	_io := fileTorrentImplIO{me}
	// Return the appropriate segments of this.
	return &filePieceImpl{
		me,
		p,
		missinggo.NewSectionWriter(_io, p.Offset(), p.Length()),
		io.NewSectionReader(_io, p.Offset(), p.Length()),
	}
}

func (me *fileTorrentImpl) Close() error {
	return me.io.Close()
}

func (me *fileTorrentImpl) file(index int) file {
	return file{
		Info:      me.info,
		FileInfo:  &me.metainfoFileInfos[index],
		fileExtra: &me.files[index],
	}
}

// Open file for reading.
func (me *fileTorrentImpl) openSharedFile(file file) (f sharableReader, err error) {
	file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	if me.partFiles() {
		f, err = me.io.openForSharedRead(file.partFilePath())
	}
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = me.io.openForSharedRead(file.safeOsPath)
	}
	file.mu.RUnlock()
	return
}

// Open file for reading. Not a shared handle if that matters.
func (me *fileTorrentImpl) openFile(file file) (f fileReader, err error) {
	file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	if me.partFiles() {
		f, err = me.io.openForRead(file.partFilePath())
	}
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = me.io.openForRead(file.safeOsPath)
	}
	file.mu.RUnlock()
	return
}

func (me *fileTorrentImpl) openForWrite(file file) (_ fileWriter, err error) {
	// It might be possible to have a writable handle shared files cache if we need it.
	me.logger().Debug("openForWrite", "file.safeOsPath", file.safeOsPath)
	return me.io.openForWrite(me.pathForWrite(&file), file.FileInfo.Length)
}
