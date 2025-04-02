package storage

import "errors"

type Zero struct{}

// Close implements TorrentImpl.
func (z Zero) Close() error {
	return nil
}

// ReadAt implements TorrentImpl.
func (z Zero) ReadAt(p []byte, off int64) (n int, err error) {
	n = copy(p, make([]byte, len(p)))
	return n, nil
}

// WriteAt implements TorrentImpl.
func (z Zero) WriteAt(p []byte, off int64) (n int, err error) {
	return 0, errors.ErrUnsupported
}

func NewZero() TorrentImpl {
	return Zero{}
}
