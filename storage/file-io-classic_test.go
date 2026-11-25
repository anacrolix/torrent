package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func TestDefaultFileIoDefaultsToMmapWithoutEnv(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "")
	qt.Assert(t, qt.IsNil(os.Unsetenv("TORRENT_STORAGE_DEFAULT_FILE_IO")))
	ioImpl := defaultFileIo(defaultFileModes)
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok := ioImpl.(*mmapFileIo)
	qt.Assert(t, qt.IsTrue(ok))
}

func TestDefaultFileIoReadsEnvironmentLazily(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "classic")
	ioImpl := defaultFileIo(defaultFileModes)
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok := ioImpl.(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))

	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "mmap")
	ioImpl = defaultFileIo(defaultFileModes)
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok = ioImpl.(*mmapFileIo)
	qt.Assert(t, qt.IsTrue(ok))
}

func TestClassicFileReaderWritesAPrefixWithoutError(t *testing.T) {
	name := filepath.Join(t.TempDir(), "data.bin")
	qt.Assert(t, qt.IsNil(os.WriteFile(name, []byte("0123456789"), 0o644)))
	f, err := os.Open(name)
	qt.Assert(t, qt.IsNil(err))
	defer f.Close()

	var buf bytes.Buffer
	written, err := classicFileReader{f}.writeToN(&buf, 4)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(written, int64(4)))
	qt.Assert(t, qt.Equals(buf.String(), "0123"))
}

type twoBytesWriter struct{ bytes.Buffer }

func (w *twoBytesWriter) Write(p []byte) (int, error) {
	return w.Buffer.Write(p[:min(len(p), 2)])
}

func TestClassicFileReaderReportsARealShortWrite(t *testing.T) {
	name := filepath.Join(t.TempDir(), "data.bin")
	qt.Assert(t, qt.IsNil(os.WriteFile(name, []byte("0123456789"), 0o644)))
	f, err := os.Open(name)
	qt.Assert(t, qt.IsNil(err))
	defer f.Close()

	var w twoBytesWriter
	written, err := classicFileReader{f}.writeToN(&w, 4)
	qt.Assert(t, qt.ErrorIs(err, io.ErrShortWrite))
	qt.Assert(t, qt.Equals(written, int64(2)))
}

func TestClassicFileIoRenameClosesCachedWriter(t *testing.T) {
	tempDir := t.TempDir()
	oldPath := filepath.Join(tempDir, "old.bin")
	newPath := filepath.Join(tempDir, "new.bin")

	ioImpl, ok := newClassicFileIo(defaultFileModes).(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))
	t.Cleanup(func() { _ = ioImpl.Close() })

	writer, err := ioImpl.openForWrite(oldPath, 0)
	qt.Assert(t, qt.IsNil(err))
	_, err = writer.WriteAt([]byte("hello"), 0)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(writer.Close()))

	qt.Assert(t, qt.IsNil(ioImpl.rename(oldPath, newPath)))

	data, err := os.ReadFile(newPath)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(data, []byte("hello")))
}

func TestClassicFileIoOpenForReadBorrowsWriterHandle(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "data.bin")

	ioImpl, ok := newClassicFileIo(defaultFileModes).(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))
	t.Cleanup(func() { _ = ioImpl.Close() })

	writer, err := ioImpl.openForWrite(filePath, 0)
	qt.Assert(t, qt.IsNil(err))
	_, err = writer.WriteAt([]byte("hello"), 0)
	qt.Assert(t, qt.IsNil(err))

	reader, err := ioImpl.openForRead(filePath)
	qt.Assert(t, qt.IsNil(err))
	defer reader.Close()

	buf := make([]byte, 5)
	n, err := reader.Read(buf)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(n, 5))
	qt.Assert(t, qt.DeepEquals(buf, []byte("hello")))
}
func TestClassicFileIoCloseWritersClearsCachedHandles(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "data.bin")

	ioImpl, ok := newClassicFileIo(defaultFileModes).(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))
	t.Cleanup(func() { _ = ioImpl.Close() })

	writer, err := ioImpl.openForWrite(filePath, 0)
	qt.Assert(t, qt.IsNil(err))
	_, err = writer.WriteAt([]byte("hello"), 0)
	qt.Assert(t, qt.IsNil(err))

	closedPaths, remaining, err := ioImpl.closeWriters()
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(closedPaths, []string{filePath}))
	qt.Assert(t, qt.Equals(remaining, 0))
	qt.Assert(t, qt.HasLen(ioImpl.writers, 0))
}

// Restoring the part file of a file that isn't complete yet finds no final file to rename. That
// must not close the cached writer for the part file, which other pieces are writing through.
func TestClassicFileIoFailedRenameKeepsCachedWriter(t *testing.T) {
	tempDir := t.TempDir()
	finalPath := filepath.Join(tempDir, "data.bin")
	partPath := finalPath + ".part"

	ioImpl, ok := newClassicFileIo(defaultFileModes).(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))
	t.Cleanup(func() { _ = ioImpl.Close() })

	writer, err := ioImpl.openForWrite(partPath, 0)
	qt.Assert(t, qt.IsNil(err))
	err = ioImpl.rename(finalPath, partPath)
	qt.Assert(t, qt.ErrorIs(err, os.ErrNotExist))
	_, err = writer.WriteAt([]byte("hello"), 0)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(writer.Close()))

	data, err := os.ReadFile(partPath)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(data, []byte("hello")))
}

// Pieces keep being written while another piece of the same file is marked not complete, as
// happens when a piece fails its hash check during a download.
func TestClassicWritesDuringMarkNotComplete(t *testing.T) {
	info := &metainfo.Info{Name: "a", Length: 2 << 14, PieceLength: 1 << 14, Pieces: make([]byte, 2*20)}
	cl := NewFileOpts(NewFileClientOpts{
		ClientBaseDir:   t.TempDir(),
		PieceCompletion: NewMapPieceCompletion(),
		DisableMmap:     true,
	})
	defer cl.Close()
	tor, err := cl.OpenTorrent(context.Background(), info, metainfo.Hash{1})
	qt.Assert(t, qt.IsNil(err))
	defer tor.Close()
	p0, p1 := tor.Piece(info.Piece(0)), tor.Piece(info.Piece(1))
	var writes atomic.Int64
	var writeErr error
	var failed atomic.Bool
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		buf := make([]byte, 1<<14)
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := p0.WriteAt(buf, 0); err != nil {
				writeErr = err
				failed.Store(true)
				return
			}
			writes.Add(1)
		}
	}()
	// Before the fix, 100000 writes were enough to hit the closed writer every time.
	for writes.Load() < 100000 && !failed.Load() {
		qt.Assert(t, qt.IsNil(p1.MarkNotComplete()))
	}
	close(done)
	<-stopped
	qt.Check(t, qt.IsNil(writeErr))
}
