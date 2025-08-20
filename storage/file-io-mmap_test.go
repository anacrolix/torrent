package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-quicktest/qt"
)

// Note this is AI slop with some modifications. Test if writes to mmap without sync or flush are
// detected by a subsequent seek data. It would appear they are since the mmap is created from the
// file descriptor used for the seek, and probably because we're in the same process so MAP_SHARED
// kicks in.
func TestFileIoImplementationsSeekDataDetection(t *testing.T) {
	testData := []byte{0x42}
	writeOffset := int64(1234567) // Large enough for sparse files to kick in.
	const fileSize = 12345678

	implementations := []struct {
		name   string
		fileIo fileIo
	}{
		{"classic", classicFileIo{}},
		{"mmap", &mmapFileIo{}},
	}

	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			// Create temporary directory and file
			tempDir := t.TempDir()
			t.Logf("tempDir: %s", tempDir)
			testFile := filepath.Join(tempDir, "test.dat")

			// Create empty file
			f, err := os.Create(testFile)
			qt.Assert(t, qt.IsNil(err))
			f.Close()

			// Open for write and write a byte without flushing
			writer, err := impl.fileIo.openForWrite(testFile, fileSize)
			qt.Assert(t, qt.IsNil(err))

			_, err = writer.WriteAt(testData, writeOffset)
			qt.Assert(t, qt.IsNil(err))

			// Close writer (but note: we didn't call flush)
			err = writer.Close()
			qt.Assert(t, qt.IsNil(err))

			// Open for read and test seekData detection
			reader, err := impl.fileIo.openForRead(testFile)
			qt.Assert(t, qt.IsNil(err))
			defer reader.Close()

			// Use seekDataOrEof to detect data
			dataPos, err := reader.seekDataOrEof(writeOffset)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(dataPos, writeOffset))

			var buf bytes.Buffer
			written, err := reader.writeToN(&buf, 1)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(written, 1))
			qt.Assert(t, qt.DeepEquals(buf.Bytes(), testData))
		})
	}
}
