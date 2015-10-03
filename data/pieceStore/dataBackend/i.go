package dataBackend

import (
	"errors"
	"io"
)

// All functions must return ErrNotFound as required.
type I interface {
	GetLength(path string) (int64, error)
	Open(path string) (File, error)
	OpenSection(path string, off, n int64) (io.ReadCloser, error)
	Delete(path string) error
}

var ErrNotFound = errors.New("not found")

type File interface {
	io.Closer
	io.Seeker
	io.Writer
}
