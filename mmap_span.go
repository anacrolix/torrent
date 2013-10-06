package torrent

import (
	"io"
	"launchpad.net/gommap"
)

type MMap struct {
	gommap.MMap
}

func (me MMap) Size() int64 {
	return int64(len(me.MMap))
}

type MMapSpan []MMap

func (me MMapSpan) span() (s span) {
	for _, mmap := range me {
		s = append(s, mmap)
	}
	return
}

func (me MMapSpan) Close() {
	for _, mMap := range me {
		mMap.UnsafeUnmap()
	}
}

func (me MMapSpan) Size() (ret int64) {
	for _, mmap := range me {
		ret += mmap.Size()
	}
	return
}

func (me MMapSpan) ReadAt(p []byte, off int64) (n int, err error) {
	me.span().ApplyTo(off, func(intervalOffset int64, interval sizer) (stop bool) {
		_n := copy(p, interval.(MMap).MMap[intervalOffset:])
		p = p[_n:]
		n += _n
		return len(p) == 0
	})
	if len(p) != 0 {
		err = io.EOF
	}
	return
}

func (me MMapSpan) WriteSectionTo(w io.Writer, off, n int64) (written int64, err error) {
	me.span().ApplyTo(off, func(intervalOffset int64, interval sizer) (stop bool) {
		var _n int
		p := interval.(MMap).MMap[intervalOffset:]
		if n < int64(len(p)) {
			p = p[:n]
		}
		_n, err = w.Write(p)
		written += int64(_n)
		n -= int64(_n)
		if err != nil {
			return true
		}
		return n != 0
	})
	return
}

func (me MMapSpan) WriteAt(p []byte, off int64) (n int, err error) {
	me.span().ApplyTo(off, func(iOff int64, i sizer) (stop bool) {
		_n := copy(i.(MMap).MMap[iOff:], p)
		p = p[_n:]
		n += _n
		return len(p) == 0
	})
	if len(p) != 0 {
		err = io.ErrShortWrite
	}
	return
}
