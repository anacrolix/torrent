package storage

import (
	"io"
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
	openForWrite(name string, size int64) (fileWriter, error)
	flush(name string, offset, nbytes int64) error
}
