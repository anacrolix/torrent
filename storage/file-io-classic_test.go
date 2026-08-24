package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"
)

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
