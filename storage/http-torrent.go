package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	_log "log"
	"log/slog"
	"os"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

type HttpTorrentImpl struct {
	info              *metainfo.Info
	files             []fileExtra
	metainfoFileInfos []metainfo.FileInfo
	segmentLocater    segments.Index
	infoHash          metainfo.Hash
	io                fileIo
	// Save memory by pointing to the other data.
	storage *HttpStorageImpl
}

func (torrent *HttpTorrentImpl) logger() *slog.Logger {
	return torrent.storage.Opts.Logger
}

func (torrent *HttpTorrentImpl) GetStorage() *HttpStorageImpl {
	return torrent.storage
}

func (torrent *HttpTorrentImpl) pieceCompletion() PieceCompletion {
	// panics if ContentFileStorage has wrong type
	fileStorage := (torrent.storage.Opts.ContentFileStorage).(*FileClientImpl)
	return fileStorage.GetOpts().PieceCompletion
}

func (torrent *HttpTorrentImpl) pieceCompletionKey(p int) metainfo.PieceKey {
	return metainfo.PieceKey{
		InfoHash: torrent.infoHash,
		Index:    p,
	}
}

func (torrent *HttpTorrentImpl) setPieceCompletion(p int, complete bool) error {
	return torrent.pieceCompletion().Set(torrent.pieceCompletionKey(p), complete)
}

// Set piece completions based on whether all files in each piece are not .part files.
func (torrent *HttpTorrentImpl) setCompletionFromPartFiles() error {
	notComplete := make([]bool, torrent.info.NumPieces())
	for fileIndex := range torrent.files {
		f := torrent.file(fileIndex)
		fi, err := os.Stat(f.safeOsPath)
		if err == nil {
			if fi.Size() == f.length() {
				continue
			}
			torrent.logger().Warn("file has unexpected size", "file", f.safeOsPath, "size", fi.Size(), "expected", f.length())
		} else if !errors.Is(err, fs.ErrNotExist) {
			torrent.logger().Warn("error checking file size", "err", err)
		}
		// Ensure all pieces associated with a file are not marked as complete (at most unknown).
		for pieceIndex := f.beginPieceIndex(); pieceIndex < f.endPieceIndex(); pieceIndex++ {
			notComplete[pieceIndex] = true
		}
	}
	for i, nc := range notComplete {
		if nc {
			c := torrent.getCompletion(i)
			if c.Complete {
				// TODO: We need to set unknown so that verification of the data we do have could
				// occur naturally but that'll be a big change.
				panicif.Err(torrent.setPieceCompletion(i, false))
			}
		} else {
			err := torrent.setPieceCompletion(i, true)
			if err != nil {
				return fmt.Errorf("setting piece %v completion: %w", i, err)
			}
		}
	}
	return nil
}

func (torrent *HttpTorrentImpl) partFiles() bool {
	return torrent.storage.Opts.partFiles()
}

func (torrent *HttpTorrentImpl) pathForWrite(f *file) string {
	if torrent.partFiles() {
		return f.partFilePath()
	}
	return f.safeOsPath
}

func (torrent *HttpTorrentImpl) getCompletion(piece int) Completion {
	cmpl, err := torrent.pieceCompletion().Get(metainfo.PieceKey{torrent.infoHash, piece})
	cmpl.Err = errors.Join(cmpl.Err, err)
	return cmpl
}

func (torrent *HttpTorrentImpl) Piece(p metainfo.Piece) PieceImpl {
	// Create a view onto the file-based torrent storage.
	_io := HttpTorrentImplIO{torrent}
	// Return the appropriate segments of this.
	return &HttpPieceImpl{
		torrent,
		p,
		missinggo.NewSectionWriter(_io, p.Offset(), p.Length()),
		io.NewSectionReader(_io, p.Offset(), p.Length()),
	}
}

func (fs *HttpTorrentImpl) Close() error {
	return nil
}

func (torrent *HttpTorrentImpl) file(index int) file {
	return file{
		Info:      torrent.info,
		FileInfo:  &torrent.metainfoFileInfos[index],
		fileExtra: &torrent.files[index],
	}
}

// Open file for reading.
func (me *HttpTorrentImpl) openSharedFile(file file) (f sharedFileIf, err error) {
	_log.Printf("HttpTorrentImpl.openSharedFile\n")
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
func (me *HttpTorrentImpl) openFile(file file) (f fileReader, err error) {
	_log.Printf("HttpTorrentImpl.openFile: file.safeOsPath=%s\n", file.safeOsPath)
	file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	if me.partFiles() {
		f, err = me.io.openForRead(file.partFilePath())
	}
	_log.Printf("HttpTorrentImpl.openFile: err1=%v\n", err)
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = me.io.openForRead(file.safeOsPath)
	}
	_log.Printf("HttpTorrentImpl.openFile: err2=%v\n", err)
	file.mu.RUnlock()
	return
}

func (fst *HttpTorrentImpl) openForWrite(file file) (_ fileWriter, err error) {
	_log.Printf("HttpTorrentImpl.openForWrite\n")
	// It might be possible to have a writable handle shared files cache if we need it.
	fst.logger().Debug("openForWrite", "file.safeOsPath", file.safeOsPath)
	return fst.io.openForWrite(fst.pathForWrite(&file), file.FileInfo.Length)
}
