package storage

import (
	"errors"
	"expvar"
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

func (me *filePieceImpl) Flush() (err error) {
	for fileIndex, extent := range me.fileExtents() {
		file := me.t.file(fileIndex)
		name := me.t.pathForWrite(&file)
		err1 := me.t.io.flush(name, extent.Start, extent.Length)
		if err1 != nil {
			err = errors.Join(err, fmt.Errorf("flushing %q:%v+%v: %w", name, extent.Start, extent.Length, err1))
			return
		}
	}
	return nil
}

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

func (me *filePieceImpl) fileExtents() iter.Seq2[int, segments.Extent] {
	return me.t.segmentLocater.LocateIter(me.extent())
}

func (me *filePieceImpl) pieceFiles() iter.Seq[file] {
	return func(yield func(file) bool) {
		for fileIndex := range me.fileExtents() {
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
		pieceExtent := me.extent()
		noFiles := true
		for i, extent := range me.t.segmentLocater.LocateIter(pieceExtent) {
			noFiles = false
			if !yield(i, extent) {
				return
			}
		}
		panicif.NotEq(noFiles, pieceExtent.Length == 0)
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
	if pieceCompletionIsPersistent(me.pieceCompletion()) {
		err := me.Flush()
		if err != nil {
			me.logger().Warn("error flushing completed piece", "piece", me.p.Index(), "err", err)
		}
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
	// Flush file on completion, even if we don't promote it.
	err = me.t.io.flush(f.partFilePath(), 0, f.length())
	if err != nil {
		me.logger().Warn("error flushing file before promotion", "file", f.partFilePath(), "err", err)
		err = nil
	}
	if !me.partFiles() {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
	renamed, err := me.exclRenameIfExists(f.partFilePath(), f.safeOsPath)
	if err != nil {
		return
	}
	if !renamed {
		return
	}
	err = os.Chmod(f.safeOsPath, filePerm&^0o222)
	if err != nil {
		me.logger().Info("error setting promoted file to read-only", "file", f.safeOsPath, "err", err)
		err = nil
	}
	return
}

// Rename from if exists, and if so, to must not exist.
func (me *filePieceImpl) exclRenameIfExists(from, to string) (renamed bool, err error) {
	err = os.Rename(from, to)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return
	}
	renamed = true
	me.logger().Debug("renamed file", "from", from, "to", to)
	return
}

func (me *filePieceImpl) onFileNotComplete(f file) (err error) {
	if !me.partFiles() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
	_, err = me.exclRenameIfExists(f.safeOsPath, f.partFilePath())
	if err != nil {
		err = fmt.Errorf("restoring part file: %w", err)
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

type zeroReader struct{}

func (me zeroReader) Read(p []byte) (n int, err error) {
	clear(p)
	return len(p), nil
}

func (me *filePieceImpl) WriteTo(w io.Writer) (n int64, err error) {
	for fileIndex, extent := range me.iterFileSegments() {
		var n1 int64
		n1, err = me.writeFileTo(w, fileIndex, extent)
		n += n1
		if err != nil {
			return
		}
		panicif.GreaterThan(n1, extent.Length)
		if n1 < extent.Length {
			return
		}
		panicif.NotEq(n1, extent.Length)
	}
	return
}

var (
	packageExpvarMap = expvar.NewMap("torrentStorage")
)

type limitWriter struct {
	rem int64
	w   io.Writer
}

func (me *limitWriter) Write(p []byte) (n int, err error) {
	n, err = me.w.Write(p[:min(int64(len(p)), me.rem)])
	me.rem -= int64(n)
	if err != nil {
		return
	}
	p = p[n:]
	if len(p) > 0 {
		err = io.ErrShortWrite
	}
	return
}

func (me *filePieceImpl) writeFileTo(w io.Writer, fileIndex int, extent segments.Extent) (written int64, err error) {
	if extent.Length == 0 {
		return
	}
	file := me.t.file(fileIndex)
	// Do we want io.WriterTo here, or are we happy to let that be type asserted in io.CopyN?
	var f fileReader
	f, err = me.t.openFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return
	}
	defer f.Close()
	panicif.GreaterThan(extent.End(), file.FileInfo.Length)
	extentRemaining := extent.Length
	var dataOffset int64
	dataOffset, err = f.seekDataOrEof(extent.Start)
	if err != nil {
		err = fmt.Errorf("seeking to start of extent: %w", err)
		return
	}
	if dataOffset < extent.Start {
		// File is too short.
		return
	}
	if dataOffset > extent.Start {
		// Write zeroes until the end of the hole we're in.
		var n1 int64
		n := min(dataOffset-extent.Start, extent.Length)
		n1, err = writeZeroes(w, n)
		packageExpvarMap.Add("bytesReadSkippedHole", n1)
		written += n1
		if err != nil {
			return
		}
		panicif.NotEq(n1, n)
		extentRemaining -= n1
	}
	n1, err := f.writeToN(w, extentRemaining)
	packageExpvarMap.Add("bytesReadNotSkipped", n1)
	written += n1
	return
}

//
//// TODO: Just implement StorageReader already.
//func (me *filePieceImpl) NewReader() (PieceReader, error) {
//
//}
