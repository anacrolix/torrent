package storage

import (
	"io"
)

type fileWriter interface {
	io.WriterAt
	io.Closer
}

type fileReader interface {
	// Seeks to the next data in the file. If there is no more data, seeks to the end of the file.
	seekDataOrEof(offset int64) (ret int64, err error)
	writeToN(w io.Writer, n int64) (written int64, err error)
	io.ReadCloser
}

// This gets clobbered by a hybrid mmap implementation if mmap is available.
var defaultFileIo = func() fileIo {
	return classicFileIo{}
}

type fileIo interface {
	Close() error

	openForSharedRead(name string) (sharableReader, error)
	openForRead(name string) (fileReader, error)
	openForWrite(name string, size int64) (fileWriter, error)
	flush(name string, offset, nbytes int64) error
	rename(from, to string) error
}

type sharableReader interface {
	io.ReaderAt
	io.Closer
}
