package torrent

import (
	"io"
	"launchpad.net/gommap"
)

type Mmap struct {
	gommap.MMap
}

func (me Mmap) Size() int64 {
	return int64(len(me.MMap))
}

type MmapSpan []Mmap

func (me MmapSpan) span() (s span) {
	for _, mmap := range me {
		s = append(s, mmap)
	}
	return
}

func (me MmapSpan) Size() (ret int64) {
	for _, mmap := range me {
		ret += mmap.Size()
	}
	return
}

func (me MmapSpan) ReadAt(p []byte, off int64) (n int, err error) {
	me.span().ApplyTo(off, func(intervalOffset int64, interval sizer) (stop bool) {
		_n := copy(p, interval.(Mmap).MMap[intervalOffset:])
		p = p[_n:]
		n += _n
		return len(p) == 0
	})
	if len(p) != 0 {
		err = io.EOF
	}
	return
}

func (me MmapSpan) WriteAt(p []byte, off int64) (n int, err error) {
	me.span().ApplyTo(off, func(iOff int64, i sizer) (stop bool) {
		_n := copy(i.(Mmap).MMap[iOff:], p)
		p = p[_n:]
		n += _n
		return len(p) == 0
	})
	if len(p) != 0 {
		err = io.EOF
	}
	return
}
