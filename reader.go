package torrent

import (
	"errors"
	"io"
	"log"
	"sync/atomic"

	"github.com/james-lawrence/torrent/storage"
)

func newBlockingReader(imp storage.TorrentImpl, c *chunks) *blockingreader {
	return &blockingreader{
		TorrentImpl: imp,
		c:           c,
	}
}

type blockingreader struct {
	storage.TorrentImpl
	c *chunks
}

func (t *blockingreader) ReadAt(p []byte, offset int64) (n int, err error) {
	log.Println("blocking reading initiated", offset)
	defer log.Println("blocking reading completed", offset)

	t.c.cond.L.Lock()
	for !t.c.ChunksCompleteForOffset(offset) {
		t.c.cond.Wait()
	}
	t.c.cond.L.Unlock()

	return t.TorrentImpl.ReadAt(p, offset)
}

// Reader for a torrent
type Reader interface {
	io.Reader
	io.Seeker
	io.Closer
}

func NewReader(t Torrent) Reader {
	return &reader{
		ReaderAt: t.Storage(),
		length:   t.Info().TotalLength(),
	}
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
	log.Println("read initiated", r.pos, r.length)
	n, err = r.ReadAt(b, r.pos)
	npos := atomic.AddInt64(&r.pos, int64(n))
	log.Println("read completed", npos, r.length)
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
