package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestDefaultFileIoDefaultsToMmapWithoutEnv(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "")
	qt.Assert(t, qt.IsNil(os.Unsetenv("TORRENT_STORAGE_DEFAULT_FILE_IO")))
	ioImpl := defaultFileIo()
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok := ioImpl.(*mmapFileIo)
	qt.Assert(t, qt.IsTrue(ok))
}

func TestDefaultFileIoReadsEnvironmentLazily(t *testing.T) {
	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "classic")
	ioImpl := defaultFileIo()
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok := ioImpl.(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))

	t.Setenv("TORRENT_STORAGE_DEFAULT_FILE_IO", "mmap")
	ioImpl = defaultFileIo()
	t.Cleanup(func() { _ = ioImpl.Close() })
	_, ok = ioImpl.(*mmapFileIo)
	qt.Assert(t, qt.IsTrue(ok))
}

func TestClassicFileIoRenameClosesCachedWriter(t *testing.T) {
	tempDir := t.TempDir()
	oldPath := filepath.Join(tempDir, "old.bin")
	newPath := filepath.Join(tempDir, "new.bin")

	ioImpl, ok := newClassicFileIo().(*classicFileIo)
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

	ioImpl, ok := newClassicFileIo().(*classicFileIo)
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

func TestClassicFileReaderWriteToNDoesNotReportShortWrite(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "data.bin")
	qt.Assert(t, qt.IsNil(os.WriteFile(filePath, []byte("hello world"), 0o600)))

	ioImpl, ok := newClassicFileIo().(*classicFileIo)
	qt.Assert(t, qt.IsTrue(ok))
	t.Cleanup(func() { _ = ioImpl.Close() })

	reader, err := ioImpl.openForRead(filePath)
	qt.Assert(t, qt.IsNil(err))
	defer reader.Close()

	var buf bytes.Buffer
	written, err := reader.writeToN(&buf, 5)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(written, int64(5)))
	qt.Assert(t, qt.DeepEquals(buf.Bytes(), []byte("hello")))
}
