package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"os"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

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

var _ interface {
	PieceImpl
	//PieceReaderer
} = (*filePieceImpl)(nil)

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
	return me.t.pieceCompletion()
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
				// Can we use shared files for this? Is it faster?
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
	for _, f := range me.pieceFiles() {
		res := me.allFilePiecesComplete(f)
		if res.Err != nil {
			err = res.Err
			return
		}
		if !res.Ok {
			continue
		}
		err = me.promotePartFile(f)
		if err != nil {
			err = fmt.Errorf("error promoting part file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return
}

func (me *filePieceImpl) allFilePiecesComplete(f file) (ret g.Result[bool]) {
	next, stop := iter.Pull(GetPieceCompletionRange(
		me.t.pieceCompletion(),
		me.t.infoHash,
		f.beginPieceIndex,
		f.endPieceIndex,
	))
	defer stop()
	for p := f.beginPieceIndex; p < f.endPieceIndex; p++ {
		cmpl, ok := next()
		panicif.False(ok)
		if cmpl.Err != nil {
			ret.Err = fmt.Errorf("error getting completion for piece %d: %w", p, cmpl.Err)
			return
		}
		if !cmpl.Ok || !cmpl.Complete {
			return
		}
	}
	_, ok := next()
	panicif.True(ok)
	ret.SetOk(true)
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
		err = me.exclRenameIfExists(f.partFilePath(), f.safeOsPath)
		if err != nil {
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
func (me *filePieceImpl) exclRenameIfExists(from, to string) error {
	if true {
		// Might be cheaper to check source exists than to create destination regardless.
		_, err := os.Stat(from)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	// We don't want anyone reading or writing to this until the rename completes.
	f, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL, 0)
	if errors.Is(err, fs.ErrExist) {
		_, err = os.Stat(from)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("source and destination files both exist")
	}
	if err != nil {
		return fmt.Errorf("exclusively creating destination file: %w", err)
	}
	f.Close()
	err = os.Rename(from, to)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Someone else has moved it already.
			return nil
		}
		// If we can't rename it, remove the blocking destination file we made. Maybe the remove
		// error should be logged separately since it's not actionable.
		return errors.Join(err, os.Remove(to))
	}
	me.logger().Debug("renamed file", "from", from, "to", to)
	return nil
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
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("statting file: %w", err)
		return
	}
	// Ensure the file is writable
	err = os.Chmod(me.pathForWrite(f), info.Mode().Perm()|(filePerm&0o222))
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

//
//// TODO: Just implement StorageReader already.
//func (me *filePieceImpl) NewReader() (PieceReader, error) {
//
//}
