package torrent

import (
	"errors"
	"io"
	"sync/atomic"
)

// Reader for a torrent
type Reader interface {
	io.Reader
	io.Seeker
	io.Closer
}

// Accesses Torrent data via a Client. Reads block until the data is
// available. Seeks and readahead also drive Client behaviour.
type reader struct {
	io.ReaderAt
	// Adjust the read/seek window to handle Readers locked to File extents
	// and the like.
	offset, length int64
	pos            int64
}

var _ io.ReadCloser = &reader{}

func (r *reader) Read(b []byte) (n int, err error) {
	n, err = r.ReadAt(b, r.pos)
	atomic.AddInt64(&r.pos, int64(n))
	return n, err
}

func (r *reader) Close() error {
	return nil
}

func (r *reader) Seek(off int64, whence int) (ret int64, err error) {
	switch whence {
	case io.SeekStart:
		return atomic.SwapInt64(&r.pos, off), nil
	case io.SeekCurrent:
		return atomic.AddInt64(&r.pos, off), nil
	case io.SeekEnd:
		return atomic.SwapInt64(&r.pos, r.length+off), nil
	default:
		return -1, errors.ErrUnsupported
	}
}
