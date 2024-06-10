package mmap_span

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/torrent/segments"
	"github.com/anacrolix/torrent/types/infohash"
)

type Mmap interface {
	Flush() error
	Unmap() error
	Bytes() []byte
}

type FlushedCallback func(infoHash infohash.T, flushed roaring.Bitmap)

type MMapSpan struct {
	mu              sync.RWMutex
	mMaps           []Mmap
	dirtyPieces     roaring.Bitmap
	segmentLocater  segments.Index
	InfoHash        infohash.T
	flushTimer      *time.Timer
	FlushTime       time.Duration
	FlushedCallback FlushedCallback
}

func (ms *MMapSpan) Append(mMap Mmap) {
	ms.mMaps = append(ms.mMaps, mMap)
}

func (ms *MMapSpan) Flush() (errs []error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if ms.flushTimer != nil {
		ms.flushTimer = time.AfterFunc(ms.FlushTime,
			func() {
				// TODO deal with logging errors
				ms.flushMaps()
			})
	}
	return
}

func (ms *MMapSpan) flushMaps() (errs []error) {
	var flushedCallback FlushedCallback
	var dirtyPieces roaring.Bitmap

	errs = func() (errs []error) {
		ms.mu.Lock()
		defer ms.mu.Unlock()
		dirtyPieces = dirtyPieces

		if ms.flushTimer != nil {
			ms.flushTimer = nil
			/*for _, mMap := range ms.mMaps {
				err := mMap.Flush()
				if err != nil {
					errs = append(errs, err)

				}
			}*/

			if len(errs) == 0 {
				flushedCallback = ms.FlushedCallback
				dirtyPieces = roaring.Bitmap{}
			}
		}

		return
	}()

	if flushedCallback != nil {
		flushedCallback(ms.InfoHash, ms.dirtyPieces)
	}

	return
}

func (ms *MMapSpan) Close() (errs []error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.flushTimer != nil {
		ms.flushTimer.Stop()
		errs = ms.flushMaps()
		ms.flushTimer = nil
	}

	for _, mMap := range ms.mMaps {
		err := mMap.Unmap()
		if err != nil {
			errs = append(errs, err)
		}
	}
	// This is for issue 211.
	ms.mMaps = nil
	ms.InitIndex()
	return
}

func (me *MMapSpan) InitIndex() {
	i := 0
	me.segmentLocater = segments.NewIndex(func() (segments.Length, bool) {
		if i == len(me.mMaps) {
			return -1, false
		}
		l := int64(len(me.mMaps[i].Bytes()))
		i++
		return l, true
	})
	// log.Printf("made mmapspan index: %v", me.segmentLocater)
}

func (ms *MMapSpan) ReadAt(p []byte, off int64) (n int, err error) {
	// log.Printf("reading %v bytes at %v", len(p), off)
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	n = ms.locateCopy(func(a, b []byte) (_, _ []byte) { return a, b }, p, off)
	if n != len(p) {
		err = io.EOF
	}
	return
}

func copyBytes(dst, src []byte) int {
	return copy(dst, src)
}

func (ms *MMapSpan) locateCopy(copyArgs func(remainingArgument, mmapped []byte) (dst, src []byte), p []byte, off int64) (n int) {
	ms.segmentLocater.Locate(segments.Extent{off, int64(len(p))}, func(i int, e segments.Extent) bool {
		mMapBytes := ms.mMaps[i].Bytes()[e.Start:]
		// log.Printf("got segment %v: %v, copying %v, %v", i, e, len(p), len(mMapBytes))
		_n := copyBytes(copyArgs(p, mMapBytes))
		p = p[_n:]
		n += _n

		if segments.Int(_n) != e.Length {
			panic(fmt.Sprintf("did %d bytes, expected to do %d", _n, e.Length))
		}
		return true
	})
	return
}

func (ms *MMapSpan) WriteAt(p []byte, off int64) (n int, err error) {
	// log.Printf("writing %v bytes at %v", len(p), off)
	n, err = func() (n int, err error) {
		ms.mu.RLock()
		defer ms.mu.RUnlock()
		n = ms.locateCopy(func(a, b []byte) (_, _ []byte) { return b, a }, p, off)
		if n != len(p) {
			err = io.ErrShortWrite
		}

		return
	}()

	if err != nil {
		return
	}

	ms.mu.Lock()
	ms.dirtyPieces.Add(uint32(int64(len(p)) / off))
	ms.mu.Unlock()

	return
}
