//go:build !wasm

package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/edsrzf/mmap-go"
)

func init() {
	s, ok := os.LookupEnv("TORRENT_STORAGE_DEFAULT_FILE_IO")
	if !ok {
		defaultFileIo = func() fileIo {
			return &mmapFileIo{}
		}
		return
	}
	switch s {
	case "mmap":
		defaultFileIo = func() fileIo {
			return &mmapFileIo{}
		}
	case "classic":
		defaultFileIo = func() fileIo {
			return classicFileIo{}
		}
	default:
		panic(s)
	}
}

type mmapFileIo struct {
	mu    sync.RWMutex
	paths map[string]*fileMmap
}

func (me *mmapFileIo) flush(name string, offset, nbytes int64) error {
	// Since we are only flushing writes that we created, and we don't currently unmap files after
	// we've opened them, then if the mmap doesn't exist yet then there's nothing to flush.
	me.mu.RLock()
	defer me.mu.RUnlock()
	v, ok := me.paths[name]
	if !ok {
		return nil
	}
	if !v.writable {
		return nil
	}
	// Darwin doesn't have sync for file-offsets?!
	return msync(v.m, int(offset), int(nbytes))
}

type fileMmap struct {
	m        mmap.MMap
	f        *os.File
	refs     atomic.Int32
	writable bool
}

func (me *fileMmap) dec() error {
	if me.refs.Add(-1) == 0 {
		return me.close()
	}
	return nil
}

func (me *fileMmap) close() (err error) {
	return errors.Join(me.m.Unmap(), me.f.Close())
}

func (me *fileMmap) inc() {
	panicif.LessThanOrEqual(me.refs.Add(1), 0)
}

func (m *mmapFileIo) openForSharedRead(name string) (_ sharedFileIf, err error) {
	return m.openReadOnly(name)
}

func (m *mmapFileIo) openForRead(name string) (_ fileReader, err error) {
	sh, err := m.openReadOnly(name)
	if err != nil {
		return
	}
	return &mmapFileHandle{
		shared: sh,
	}, nil
}

func (m *mmapFileIo) openReadOnly(name string) (_ *mmapSharedFileHandle, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.paths[name]
	if ok {
		return newMmapFile(v), nil
	}
	f, err := os.Open(name)
	if err != nil {
		return
	}
	mm, err := mmap.Map(f, mmap.RDONLY, 0)
	if err != nil {
		f.Close()
		err = fmt.Errorf("mapping file: %w", err)
		return
	}
	v = m.addNewMmap(name, mm, false, f)
	return newMmapFile(v), nil
}

func (m *mmapFileIo) openForWrite(name string, size int64) (_ fileWriter, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.paths[name]
	if ok {
		if int64(len(v.m)) == size && v.writable {
			v.inc()
			return newMmapFile(v), nil
		} else {
			v.dec()
			g.MustDelete(m.paths, name)
		}
	}
	// TODO: A bunch of this can be done without holding the lock.
	f, err := openFileExtra(name, os.O_RDWR)
	if err != nil {
		return
	}
	closeFile := true
	defer func() {
		if closeFile {
			f.Close()
		}
	}()
	err = f.Truncate(size)
	if err != nil {
		err = fmt.Errorf("error truncating file: %w", err)
		return
	}
	mm, err := mmap.Map(f, mmap.RDWR, 0)
	if err != nil {
		return
	}
	// This can happen due to filesystem changes outside our control. Don't be naive.
	if int64(len(mm)) != size {
		err = fmt.Errorf("new mmap has wrong size %v, expected %v", len(mm), size)
		mm.Unmap()
		return
	}
	closeFile = false
	return newMmapFile(m.addNewMmap(name, mm, true, f)), nil
}

func newMmapFile(f *fileMmap) *mmapSharedFileHandle {
	ret := &mmapSharedFileHandle{
		f: f,
	}
	ret.f.inc()
	return ret
}

func (me *mmapFileIo) addNewMmap(name string, mm mmap.MMap, writable bool, f *os.File) *fileMmap {
	v := &fileMmap{
		m:        mm,
		f:        f,
		writable: writable,
	}
	// One for the store, one for the caller.
	v.refs.Store(1)
	g.MakeMapIfNil(&me.paths)
	g.MapMustAssignNew(me.paths, name, v)
	return v
}

var _ fileIo = (*mmapFileIo)(nil)

type mmapSharedFileHandle struct {
	f     *fileMmap
	close sync.Once
}

func (m *mmapSharedFileHandle) WriteAt(p []byte, off int64) (n int, err error) {
	// It's not actually worth the hassle to write using mmap here since the caller provided the
	// buffer already.
	return m.f.f.WriteAt(p, off)
}

func (m *mmapSharedFileHandle) ReadAt(p []byte, off int64) (n int, err error) {
	n = copy(p, m.f.m[off:])
	if n < len(p) {
		if off < 0 {
			err = fs.ErrInvalid
			return
		}
	}
	if off+int64(n) == int64(len(m.f.m)) {
		err = io.EOF
	}
	return
}

func (m *mmapSharedFileHandle) Close() (err error) {
	m.close.Do(func() {
		err = m.f.dec()
	})
	return
}

type mmapFileHandle struct {
	shared *mmapSharedFileHandle
	pos    int64
}

func (me *mmapFileHandle) WriteTo(w io.Writer) (n int64, err error) {
	b := me.shared.f.m
	if me.pos >= int64(len(b)) {
		return
	}
	n1, err := w.Write(b[me.pos:])
	n = int64(n1)
	me.pos += n
	return
}

func (me *mmapFileHandle) writeToN(w io.Writer, n int64) (written int64, err error) {
	b := me.shared.f.m
	if me.pos >= int64(len(b)) {
		return
	}
	b = b[me.pos:]
	b = b[:min(int64(len(b)), n)]
	i, err := w.Write(b)
	written = int64(i)
	me.pos += written
	return
}

func (me *mmapFileHandle) Close() error {
	return me.shared.Close()
}

func (me *mmapFileHandle) Read(p []byte) (n int, err error) {
	if me.pos > int64(len(me.shared.f.m)) {
		err = io.EOF
		return
	}
	n = copy(p, me.shared.f.m[me.pos:])
	me.pos += int64(n)
	if me.pos >= int64(len(me.shared.f.m)) {
		err = io.EOF
	}
	return
}

func (me *mmapFileHandle) seekDataOrEof(offset int64) (ret int64, err error) {
	// This should be fine as it's an atomic operation, on a shared file handle, so nobody will be
	// relying non-atomic operations on the file. TODO: Does this require msync first so we don't
	// skip our own writes.
	ret, err = seekData(me.shared.f.f, offset)
	if err == nil {
		me.pos = ret
	} else if err == io.EOF {
		err = nil
		ret = int64(len(me.shared.f.m))
		me.pos = ret
	} else {
		ret = me.pos
	}
	return
}
