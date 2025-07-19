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

// Piece within File storage. This is created on demand.
type filePieceImpl struct {
	t *fileTorrentImpl
	p metainfo.Piece
	io.WriterAt
	io.ReaderAt
}

var _ interface {
	PieceImpl
	//PieceReaderer
	io.WriterTo
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

func (me *filePieceImpl) pieceFiles() iter.Seq[file] {
	return func(yield func(file) bool) {
		for fileIndex := range me.t.segmentLocater.LocateIter(me.extent()) {
			f := me.t.file(fileIndex)
			if !yield(f) {
				return
			}
		}
	}
}

func (me *filePieceImpl) pieceCompletion() PieceCompletion {
	return me.t.pieceCompletion()
}

func (me *filePieceImpl) Completion() (c Completion) {
	c = me.t.getCompletion(me.p.Index())
	if !c.Ok || c.Err != nil {
		return c
	}
	if c.Complete {
		c = me.checkCompleteFileSizes()
	}
	return
}

func (me *filePieceImpl) iterFileSegments() iter.Seq2[int, segments.Extent] {
	return func(yield func(int, segments.Extent) bool) {
		noFiles := true
		for i, extent := range me.t.segmentLocater.LocateIter(me.extent()) {
			noFiles = false
			if !yield(i, extent) {
				return
			}
		}
		if noFiles {
			panic("files do not cover piece extent")
		}
	}
}

// If a piece is complete, check constituent files have the minimum required sizes.
func (me *filePieceImpl) checkCompleteFileSizes() (c Completion) {
	c.Complete = true
	c.Ok = true
	for i, extent := range me.iterFileSegments() {
		file := me.t.file(i)
		file.mu.RLock()
		s, err := os.Stat(file.safeOsPath)
		if me.partFiles() && errors.Is(err, fs.ErrNotExist) {
			// Can we use shared files for this? Is it faster?
			s, err = os.Stat(file.partFilePath())
		}
		file.mu.RUnlock()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				me.logger().Warn(
					"error checking file size for piece marked as complete",
					"file", file.safeOsPath,
					"piece", me.p.Index(),
					"err", err)
				c.Complete = false
				me.markIncompletePieces(&file, 0)
				return
			}
			c.Err = fmt.Errorf("checking file %v: %w", file.safeOsPath, err)
			c.Complete = false
			return
		}
		if s.Size() < extent.End() {
			me.logger().Warn(
				"file too small for piece marked as complete",
				"piece", me.p.Index(),
				"file", file.safeOsPath,
				"size", s.Size(),
				"extent", extent)
			me.markIncompletePieces(&file, s.Size())
			c.Complete = false
			return
		}
	}
	return
}

func (me *filePieceImpl) markIncompletePieces(file *file, size int64) {
	if size >= file.length() {
		return
	}
	pieceLength := me.t.info.PieceLength
	begin := metainfo.PieceIndex((file.torrentOffset() + size) / pieceLength)
	end := metainfo.PieceIndex((file.torrentOffset() + file.length() + pieceLength - 1) / pieceLength)
	for p := begin; p < end; p++ {
		key := metainfo.PieceKey{
			InfoHash: me.t.infoHash,
			Index:    p,
		}
		err := me.pieceCompletion().Set(key, false)
		if err != nil {
			me.logger().Error("error marking piece not complete", "piece", p, "err", err)
			return
		}
	}
}

func (me *filePieceImpl) MarkComplete() (err error) {
	err = me.pieceCompletion().Set(me.pieceKey(), true)
	if err != nil {
		return
	}
	for f := range me.pieceFiles() {
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
		f.beginPieceIndex(),
		f.endPieceIndex(),
	))
	defer stop()
	for p := f.beginPieceIndex(); p < f.endPieceIndex(); p++ {
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
	for f := range me.pieceFiles() {
		err = me.onFileNotComplete(f)
		if err != nil {
			err = fmt.Errorf("preparing incomplete file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return

}

func (me *filePieceImpl) promotePartFile(f file) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
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
	panicif.Eq(from, to)
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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
	if me.partFiles() {
		err = me.exclRenameIfExists(f.safeOsPath, f.partFilePath())
		if err != nil {
			err = fmt.Errorf("restoring part file: %w", err)
			return
		}
	}
	info, err := os.Stat(me.pathForWrite(&f))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		err = fmt.Errorf("statting file: %w", err)
		return
	}
	// Ensure the file is writable
	err = os.Chmod(me.pathForWrite(&f), info.Mode().Perm()|(filePerm&0o222))
	if err != nil {
		err = fmt.Errorf("setting file writable: %w", err)
		return
	}
	return
}

func (me *filePieceImpl) pathForWrite(f *file) string {
	return me.t.pathForWrite(f)
}

func (me *filePieceImpl) partFiles() bool {
	return me.t.partFiles()
}

func (me *filePieceImpl) WriteTo(w io.Writer) (n int64, err error) {
	for fileIndex, extent := range me.iterFileSegments() {
		file := me.t.file(fileIndex)
		var f *os.File
		f, err = me.t.openFile(file)
		if err != nil {
			return
		}
		f.Seek(extent.Start, io.SeekStart)
		var n1 int64
		n1, err = io.CopyN(w, f, extent.Length)
		n += n1
		f.Close()
		if err != nil {
			return
		}
	}
	return
}

//
//// TODO: Just implement StorageReader already.
//func (me *filePieceImpl) NewReader() (PieceReader, error) {
//
//}
