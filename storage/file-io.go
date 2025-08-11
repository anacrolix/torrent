package storage

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type fileWriter interface {
	io.WriterAt
	io.Closer
}

type fileReader interface {
	// Seeks to the next data in the file. If hole-seeking/sparse-files are not supported, should
	// seek to the offset.
	seekData(offset int64) (ret int64, err error)
	io.WriterTo
	io.ReadCloser
}

type fileIo interface {
	openForSharedRead(name string) (sharedFileIf, error)
	openForRead(name string) (fileReader, error)
	openForWrite(name string) (fileWriter, error)
}

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

func (classicFileIo) openForWrite(p string) (f fileWriter, err error) {
	f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE, filePerm)
	if err == nil {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = os.MkdirAll(filepath.Dir(p), dirPerm)
		if err != nil {
			return
		}
	} else if errors.Is(err, fs.ErrPermission) {
		err = os.Chmod(p, filePerm)
		if err != nil {
			return
		}
	} else {
		return
	}
	f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE, filePerm)
	return
}
