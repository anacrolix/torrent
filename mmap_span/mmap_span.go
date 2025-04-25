package mmap_span

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"github.com/anacrolix/torrent/segments"
)

type Mmap interface {
	Flush() error
	Unmap() error
	Bytes() []byte
}

type MMapSpan struct {
	mu             sync.RWMutex
	closed         bool
	mMaps          []Mmap
	segmentLocater segments.Index
}

func New(mMaps []Mmap, index segments.Index) *MMapSpan {
	return &MMapSpan{
		mMaps:          mMaps,
		segmentLocater: index,
	}
}

func (ms *MMapSpan) Flush() (errs []error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	for _, mMap := range ms.mMaps {
		err := mMap.Flush()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return
}

func (ms *MMapSpan) Close() (err error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for _, mMap := range ms.mMaps {
		err = errors.Join(err, mMap.Unmap())
	}
	// This is for issue 211.
	ms.mMaps = nil
	ms.closed = true
	return
}

func (ms *MMapSpan) ReadAt(p []byte, off int64) (n int, err error) {
	// log.Printf("reading %v bytes at %v", len(p), off)
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if ms.closed {
		err = fs.ErrClosed
	}
	n = ms.locateCopy(func(a, b []byte) (_, _ []byte) { return a, b }, p, off)
	if n != len(p) {
		err = io.EOF
	}
	return
}

func copyBytes(dst, src []byte) int {
	return copy(dst, src)
}

func (ms *MMapSpan) locateCopy(
	copyArgs func(remainingArgument, mmapped []byte) (dst, src []byte),
	p []byte,
	off int64,
) (n int) {
	ms.segmentLocater.Locate(
		segments.Extent{off, int64(len(p))},
		func(i int, e segments.Extent) bool {
			mMapBytes := ms.mMaps[i].Bytes()[e.Start:]
			// log.Printf("got segment %v: %v, copying %v, %v", i, e, len(p), len(mMapBytes))
			_n := copyBytes(copyArgs(p, mMapBytes))
			p = p[_n:]
			n += _n

			if segments.Int(_n) != e.Length {
				panic(fmt.Sprintf("did %d bytes, expected to do %d", _n, e.Length))
			}
			return true
		},
	)
	return
}

func (ms *MMapSpan) WriteAt(p []byte, off int64) (n int, err error) {
	// log.Printf("writing %v bytes at %v", len(p), off)
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	n = ms.locateCopy(func(a, b []byte) (_, _ []byte) { return b, a }, p, off)
	if n != len(p) {
		err = io.ErrShortWrite
	}
	return
}
