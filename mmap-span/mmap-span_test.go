package mmapSpan

import (
	"io/fs"
	"slices"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/segments"
)

// fakeMmap is an in-memory Mmap for tests.
type fakeMmap struct {
	b []byte
}

func (me *fakeMmap) Flush() error { return nil }

func (me *fakeMmap) Unmap() error { return nil }

func (me *fakeMmap) Bytes() []byte { return me.b }

// newTestSpan builds an MMapSpan backed by a single mmap of the given size.
func newTestSpan(size int) (*MMapSpan, *fakeMmap) {
	mm := &fakeMmap{b: make([]byte, size)}
	index := segments.NewIndex(slices.Values([]segments.Length{int64(size)}))
	return New([]Mmap{mm}, index), mm
}

// Regression test: WriteAt after Close must return fs.ErrClosed rather than
// panicking on the nil-ed out mMaps slice, mirroring ReadAt's behaviour.
func TestWriteAtAfterClose(t *testing.T) {
	ms, _ := newTestSpan(16)
	qt.Assert(t, qt.IsNil(ms.Close()))

	n, err := ms.WriteAt([]byte("hello"), 0)
	qt.Check(t, qt.Equals(n, 0))
	qt.Check(t, qt.ErrorIs(err, fs.ErrClosed))
}

// For parity, confirm ReadAt after Close behaves the same way.
func TestReadAtAfterClose(t *testing.T) {
	ms, _ := newTestSpan(16)
	qt.Assert(t, qt.IsNil(ms.Close()))

	n, err := ms.ReadAt(make([]byte, 5), 0)
	qt.Check(t, qt.Equals(n, 0))
	qt.Check(t, qt.ErrorIs(err, fs.ErrClosed))
}

// Sanity check that an open span round-trips data through WriteAt/ReadAt.
func TestWriteReadRoundTrip(t *testing.T) {
	ms, mm := newTestSpan(16)

	n, err := ms.WriteAt([]byte("hello"), 2)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(n, 5))
	qt.Check(t, qt.DeepEquals(mm.b[2:7], []byte("hello")))

	buf := make([]byte, 5)
	n, err = ms.ReadAt(buf, 2)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(n, 5))
	qt.Check(t, qt.DeepEquals(buf, []byte("hello")))
}
