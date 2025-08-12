package storage

import (
	"os"
)

type classicFileIo struct{}

func (i classicFileIo) openForSharedRead(name string) (sharedFileIf, error) {
	return sharedFiles.Open(name)
}

type classicFileReader struct {
	*os.File
}

func (c classicFileReader) seekData(offset int64) (ret int64, err error) {
	return seekData(c.File, offset)
}

func (i classicFileIo) openForRead(name string) (fileReader, error) {
	f, err := os.Open(name)
	return classicFileReader{f}, err
}

func (classicFileIo) openForWrite(p string, size int64) (f fileWriter, err error) {
	return openFileExtra(p, os.O_WRONLY)
}
