package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"os"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

// Piece within File storage.
type filePieceImpl struct {
	t *fileTorrentImpl
	p metainfo.Piece
	io.WriterAt
	io.ReaderAt
}

var _ PieceImpl = (*filePieceImpl)(nil)

func (me *filePieceImpl) logger() *slog.Logger {
	return me.t.client.opts.Logger
}

func (me *filePieceImpl) pieceKey() metainfo.PieceKey {
	return metainfo.PieceKey{me.t.infoHash, me.p.Index()}
}

func (me *filePieceImpl) extent() segments.Extent {
	return segments.Extent{
		Start:  me.p.Offset(),
		Length: me.p.Length(),
	}
}

func (me *filePieceImpl) pieceFiles() iter.Seq2[int, file] {
	return func(yield func(int, file) bool) {
		for fileIndex := range me.t.segmentLocater.LocateIter(me.extent()) {
			if !yield(fileIndex, me.t.files[fileIndex]) {
				return
			}
		}
	}
}

func (me *filePieceImpl) pieceCompletion() PieceCompletion {
	return me.t.client.opts.PieceCompletion
}

func (me *filePieceImpl) Completion() Completion {
	c := me.t.getCompletion(me.p.Index())
	if !c.Ok || c.Err != nil {
		return c
	}
	verified := true
	if c.Complete {
		noFiles := true
		// If it's allegedly complete, check that its constituent files have the necessary length.
		for i, extent := range me.t.segmentLocater.LocateIter(me.extent()) {
			noFiles = false
			file := me.t.files[i]
			s, err := os.Stat(file.partFilePath())
			if errors.Is(err, fs.ErrNotExist) {
				s, err = os.Stat(file.safeOsPath)
			}
			if err != nil {
				me.logger().Warn(
					"error checking file for piece marked as complete",
					"piece", me.p,
					"file", file.safeOsPath,
					"err", err)
			} else if s.Size() < extent.End() {
				me.logger().Error(
					"file too small for piece marked as complete",
					"piece", me.p,
					"file", file.safeOsPath,
					"size", s.Size(),
					"extent", extent)
			} else {
				continue
			}
			verified = false
			break
		}
		// This probably belongs in a wrapper helper of some kind. I will retain the logic for now.
		if noFiles {
			panic("files do not cover piece extent")
		}
	}

	if !verified {
		// The completion was wrong, fix it.
		err := me.MarkNotComplete()
		if err != nil {
			c.Err = fmt.Errorf("error marking piece not complete: %w", err)
		}
	}

	return c
}

func (me *filePieceImpl) MarkComplete() (err error) {
	err = me.pieceCompletion().Set(me.pieceKey(), true)
	if err != nil {
		return
	}
nextFile:
	for i, f := range me.pieceFiles() {
		for p := f.beginPieceIndex; p < f.endPieceIndex; p++ {
			_ = i
			//fmt.Printf("%v %#v %v\n", i, f, p)
			cmpl := me.t.getCompletion(p)
			if cmpl.Err != nil {
				err = fmt.Errorf("error getting completion for piece %d: %w", p, cmpl.Err)
				return
			}
			if !cmpl.Ok || !cmpl.Complete {
				continue nextFile
			}
		}
		err = me.promotePartFile(f)
		if err != nil {
			err = fmt.Errorf("error promoting part file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return
}

func (me *filePieceImpl) MarkNotComplete() (err error) {
	err = me.pieceCompletion().Set(me.pieceKey(), false)
	if err != nil {
		return
	}
	for i, f := range me.pieceFiles() {
		_ = i
		err = me.onFileNotComplete(f)
		if err != nil {
			err = fmt.Errorf("preparing incomplete file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return

}

func (me *filePieceImpl) promotePartFile(f file) (err error) {
	if me.partFiles() {
		err = os.Rename(f.partFilePath(), f.safeOsPath)
		// If we get ENOENT, the file may already be in the final location.
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("renaming part file: %w", err)
			return
		}
	}
	info, err := os.Stat(f.safeOsPath)
	if err != nil {
		err = fmt.Errorf("statting file: %w", err)
		return
	}
	// Clear writability for the file.
	err = os.Chmod(f.safeOsPath, info.Mode().Perm()&^0o222)
	if err != nil {
		err = fmt.Errorf("setting file to read-only: %w", err)
		return
	}
	return
}

// Rename from if exists, and if so, to must not exist.
func (me *filePieceImpl) exclRenameIfExists(from, to string) (err error) {
	_, err = os.Stat(from)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	// We don't want anyone reading or writing to this until the rename completes.
	f, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL, 0)
	if err != nil {
		return fmt.Errorf("error creating destination file: %w", err)
	}
	f.Close()
	return os.Rename(from, to)
}

func (me *filePieceImpl) onFileNotComplete(f file) (err error) {
	if me.partFiles() {
		err = me.exclRenameIfExists(f.safeOsPath, f.partFilePath())
		if err != nil {
			err = fmt.Errorf("restoring part file: %w", err)
			return
		}
	}
	info, err := os.Stat(me.pathForWrite(f))
	if err != nil {
		err = fmt.Errorf("statting file: %w", err)
		return
	}
	// Ensure the file is writable
	err = os.Chmod(f.safeOsPath, info.Mode().Perm()|(filePerm&0o222))
	if err != nil {
		err = fmt.Errorf("setting file writable: %w", err)
		return
	}
	return
}

func (me *filePieceImpl) pathForWrite(f file) string {
	return me.t.pathForWrite(f)
}

func (me *filePieceImpl) partFiles() bool {
	return me.t.partFiles()
}
