package storage

import (
	"io"
	"os"
)

type classicFileIo struct{}

func (me classicFileIo) flush(name string, offset, nbytes int64) error {
	return fsync(name)
}

func (me classicFileIo) openForSharedRead(name string) (sharedFileIf, error) {
	return sharedFiles.Open(name)
}

func (me classicFileIo) openForRead(name string) (fileReader, error) {
	f, err := os.Open(name)
	return classicFileReader{f}, err
}

func (classicFileIo) openForWrite(p string, size int64) (f fileWriter, err error) {
	return openFileExtra(p, os.O_WRONLY)
}

type classicFileReader struct {
	*os.File
}

func (c classicFileReader) writeToN(w io.Writer, n int64) (written int64, err error) {
	lw := limitWriter{
		rem: n,
		w:   w,
	}
	return c.File.WriteTo(&lw)
}

func (c classicFileReader) seekDataOrEof(offset int64) (ret int64, err error) {
	ret, err = seekData(c.File, offset)
	if err == io.EOF {
		ret, err = c.File.Seek(0, io.SeekEnd)
	}
	return
}
