package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	_log "log"
	"log/slog"
	"os"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

// Piece within File storage. This is created on demand.
type HttpPieceImpl struct {
	torrent *HttpTorrentImpl
	piece   metainfo.Piece
	io.WriterAt
	io.ReaderAt
}

var _ interface {
	PieceImpl
	//PieceReaderer
	io.WriterTo
} = (*HttpPieceImpl)(nil)

func (piece *HttpPieceImpl) Flush() (err error) {
	for fileIndex, extent := range piece.fileExtents() {
		file := piece.torrent.file(fileIndex)
		name := piece.torrent.pathForWrite(&file)
		err1 := piece.torrent.io.flush(name, extent.Start, extent.Length)
		if err1 != nil {
			err = errors.Join(err, fmt.Errorf("flushing %q:%v+%v: %w", name, extent.Start, extent.Length, err1))
			return
		}
	}
	return nil
}

func (piece *HttpPieceImpl) logger() *slog.Logger {
	return piece.torrent.storage.Opts.Logger
}

func (piece *HttpPieceImpl) pieceKey() metainfo.PieceKey {
	return metainfo.PieceKey{piece.torrent.infoHash, piece.piece.Index()}
}

func (piece *HttpPieceImpl) extent() segments.Extent {
	return segments.Extent{
		Start:  piece.piece.Offset(),
		Length: piece.piece.Length(),
	}
}

func (piece *HttpPieceImpl) fileExtents() iter.Seq2[int, segments.Extent] {
	return piece.torrent.segmentLocater.LocateIter(piece.extent())
}

func (piece *HttpPieceImpl) pieceFiles() iter.Seq[file] {
	return func(yield func(file) bool) {
		for fileIndex := range piece.fileExtents() {
			f := piece.torrent.file(fileIndex)
			if !yield(f) {
				return
			}
		}
	}
}

func (piece *HttpPieceImpl) pieceCompletion() PieceCompletion {
	return piece.torrent.pieceCompletion()
}

func (piece *HttpPieceImpl) Completion() (c Completion) {
	c = piece.torrent.getCompletion(piece.piece.Index())
	if !c.Ok || c.Err != nil {
		return c
	}
	if c.Complete {
		c = piece.checkCompleteFileSizes()
	}
	return
}

func (piece *HttpPieceImpl) iterFileSegments() iter.Seq2[int, segments.Extent] {
	return func(yield func(int, segments.Extent) bool) {
		pieceExtent := piece.extent()
		noFiles := true
		for i, extent := range piece.torrent.segmentLocater.LocateIter(pieceExtent) {
			noFiles = false
			if !yield(i, extent) {
				return
			}
		}
		panicif.NotEq(noFiles, pieceExtent.Length == 0)
	}
}

// If a piece is complete, check constituent files have the minimum required sizes.
func (piece *HttpPieceImpl) checkCompleteFileSizes() (c Completion) {
	c.Complete = true
	c.Ok = true
	for i, extent := range piece.iterFileSegments() {
		file := piece.torrent.file(i)
		file.mu.RLock()
		s, err := os.Stat(file.safeOsPath)
		if piece.partFiles() && errors.Is(err, fs.ErrNotExist) {
			// Can we use shared files for this? Is it faster?
			s, err = os.Stat(file.partFilePath())
		}
		file.mu.RUnlock()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				piece.logger().Warn(
					"error checking file size for piece marked as complete",
					"file", file.safeOsPath,
					"piece", piece.piece.Index(),
					"err", err)
				c.Complete = false
				piece.markIncompletePieces(&file, 0)
				return
			}
			c.Err = fmt.Errorf("checking file %v: %w", file.safeOsPath, err)
			c.Complete = false
			return
		}
		if s.Size() < extent.End() {
			piece.logger().Warn(
				"file too small for piece marked as complete",
				"piece", piece.piece.Index(),
				"file", file.safeOsPath,
				"size", s.Size(),
				"extent", extent)
			piece.markIncompletePieces(&file, s.Size())
			c.Complete = false
			return
		}
	}
	return
}

func (piece *HttpPieceImpl) markIncompletePieces(file *file, size int64) {
	if size >= file.length() {
		return
	}
	pieceLength := piece.torrent.info.PieceLength
	begin := metainfo.PieceIndex((file.torrentOffset() + size) / pieceLength)
	end := metainfo.PieceIndex((file.torrentOffset() + file.length() + pieceLength - 1) / pieceLength)
	for p := begin; p < end; p++ {
		key := metainfo.PieceKey{
			InfoHash: piece.torrent.infoHash,
			Index:    p,
		}
		err := piece.pieceCompletion().Set(key, false)
		if err != nil {
			piece.logger().Error("error marking piece not complete", "piece", p, "err", err)
			return
		}
	}
}

func (piece *HttpPieceImpl) MarkComplete() (err error) {
	err = piece.pieceCompletion().Set(piece.pieceKey(), true)
	if err != nil {
		return
	}
	if pieceCompletionIsPersistent(piece.pieceCompletion()) {
		err := piece.Flush()
		if err != nil {
			piece.logger().Warn("error flushing completed piece", "piece", piece.piece.Index(), "err", err)
		}
	}
	for f := range piece.pieceFiles() {
		res := piece.allFilePiecesComplete(f)
		if res.Err != nil {
			err = res.Err
			return
		}
		if !res.Ok {
			continue
		}
		err = piece.promotePartFile(f)
		if err != nil {
			err = fmt.Errorf("error promoting part file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return
}

func (piece *HttpPieceImpl) allFilePiecesComplete(f file) (ret generics.Result[bool]) {
	next, stop := iter.Pull(GetPieceCompletionRange(
		piece.torrent.pieceCompletion(),
		piece.torrent.infoHash,
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

func (piece *HttpPieceImpl) MarkNotComplete() (err error) {
	err = piece.pieceCompletion().Set(piece.pieceKey(), false)
	if err != nil {
		return
	}
	for f := range piece.pieceFiles() {
		err = piece.onFileNotComplete(f)
		if err != nil {
			err = fmt.Errorf("preparing incomplete file %q: %w", f.safeOsPath, err)
			return
		}
	}
	return

}

func (piece *HttpPieceImpl) promotePartFile(f file) (err error) {
	// Flush file on completion, even if we don't promote it.
	err = piece.torrent.io.flush(f.partFilePath(), 0, f.length())
	if err != nil {
		piece.logger().Warn("error flushing file before promotion", "file", f.partFilePath(), "err", err)
		err = nil
	}
	if !piece.partFiles() {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
	renamed, err := piece.exclRenameIfExists(f.partFilePath(), f.safeOsPath)
	if err != nil {
		return
	}
	if !renamed {
		return
	}
	err = os.Chmod(f.safeOsPath, filePerm&^0o222)
	if err != nil {
		piece.logger().Info("error setting promoted file to read-only", "file", f.safeOsPath, "err", err)
		err = nil
	}
	return
}

// Rename from if exists, and if so, to must not exist.
func (piece *HttpPieceImpl) exclRenameIfExists(from, to string) (renamed bool, err error) {
	err = os.Rename(from, to)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return
	}
	renamed = true
	piece.logger().Debug("renamed file", "from", from, "to", to)
	return
}

func (piece *HttpPieceImpl) onFileNotComplete(f file) (err error) {
	if !piece.partFiles() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.race++
	_, err = piece.exclRenameIfExists(f.safeOsPath, f.partFilePath())
	if err != nil {
		err = fmt.Errorf("restoring part file: %w", err)
		return
	}
	return
}

func (piece *HttpPieceImpl) pathForWrite(f *file) string {
	return piece.torrent.pathForWrite(f)
}

func (piece *HttpPieceImpl) partFiles() bool {
	return piece.torrent.partFiles()
}

func (piece *HttpPieceImpl) WriteTo(w io.Writer) (n int64, err error) {
	// FIXME this should not be called.
	// torrentClient should assume that we have all pieces
	// t.SetHaveAllPieces()
	pieceCachePath := fmt.Sprintf(
		"%s/%s/%d",
		piece.torrent.GetStorage().Opts.PieceCacheDir,
		piece.torrent.infoHash.HexString(),
		piece.piece.Index(),
	)
	_log.Printf("HttpPieceImpl.WriteTo: %s\n", pieceCachePath)

	// panicif.Eq(1, 1) // print stack trace: why was WriteTo called?

	// panic: 1 == 1
	// goroutine 16 [running]:
	// github.com/anacrolix/missinggo/v2/panicif.Eq[...](...)
	//         /home/user/go/pkg/mod/github.com/anacrolix/missinggo/v2@v2.10.0/panicif/panicif.go:61
	// github.com/anacrolix/torrent/storage.(*HttpPieceImpl).WriteTo(0xc0001f0f00, {0xd2e280?, 0x0?})
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/storage/http-piece.go:315 +0x27a
	// github.com/anacrolix/torrent/storage.Piece.WriteTo({{0xe68b60?, 0xc0001f0f00?}, {0xc0000001c0?, 0x16?}}, {0x7f2392509ff8, 0xc0000e6d90})
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/storage/wrappers.go:71 +0x1b0
	// github.com/anacrolix/torrent.(*Torrent).hashPieceWithSpecificHash(0xc000272008, 0x0, {0xe68b20, 0xc0000e6d90})
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/torrent.go:1332 +0x1c5
	// github.com/anacrolix/torrent.(*Torrent).hashPiece(0xc000272008, 0x0)
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/torrent.go:1258 +0x392
	// github.com/anacrolix/torrent.(*Torrent).finishHash(0xc000272008, 0x0)
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/torrent.go:2817 +0x52
	// github.com/anacrolix/torrent.(*Torrent).pieceHasher(0xc000272008, 0x1?)
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/torrent.go:2751 +0x1c
	// created by github.com/anacrolix/torrent.(*Torrent).startSinglePieceHasher in goroutine 1
	//         /home/user/src/milahu/btcache-go/anacrolix-torrent/torrent.go:2745 +0x7f

	// // Open file for reading. Not a shared handle if that matters.
	// func (me *HttpTorrentImpl) openFile(file file) (f fileReader, err error) {
	// _log.Printf("HttpTorrentImpl.openFile: file.safeOsPath=%s\n", file.safeOsPath)
	// TODO create file from pieceCachePath
	// file.mu.RLock()
	// Fine to open once under each name on a unix system. We could make the shared file keys more
	// constrained, but it shouldn't matter. TODO: Ensure at most one of the names exist.
	_, err = piece.torrent.io.openForRead(pieceCachePath)
	if errors.Is(err, fs.ErrNotExist) {
		_log.Printf("HttpPieceImpl.WriteTo: no such file: %s\n", pieceCachePath)
	} else {
		_log.Printf("HttpPieceImpl.WriteTo: err=%v\n", err)
	}
	// file.mu.RUnlock()
	return
	// for fileIndex, extent := range piece.iterFileSegments() {
	// 	var n1 int64
	// 	n1, err = piece.writeFileTo(w, fileIndex, extent)
	// 	n += n1
	// 	if err != nil {
	// 		return
	// 	}
	// 	panicif.GreaterThan(n1, extent.Length)
	// 	if n1 < extent.Length {
	// 		return
	// 	}
	// 	panicif.NotEq(n1, extent.Length)
	// }
	// return
}

func (piece *HttpPieceImpl) writeFileTo(w io.Writer, fileIndex int, extent segments.Extent) (written int64, err error) {
	_log.Printf("HttpPieceImpl.writeFileTo\n")
	if extent.Length == 0 {
		return
	}
	file := piece.torrent.file(fileIndex)
	// Do we want io.WriterTo here, or are we happy to let that be type asserted in io.CopyN?
	var f fileReader
	f, err = piece.torrent.openFile(file)
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
//func (piece *HttpPieceImpl) NewReader() (PieceReader, error) {
//
//}
