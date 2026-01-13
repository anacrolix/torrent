//go:build !wasm

package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync/atomic"

	"github.com/anacrolix/sync"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/edsrzf/mmap-go"
)

// Lock uses of shared handles, instead of having a lifetime RLock. Because sync.RWMutex is not safe
// for recursive RLocks, you can't have both.
const lockHandleOperations = false

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
	mu sync.RWMutex
	// We could automatically expire fileMmaps by using weak.Pointers? Currently, the store never
	// relinquishes its extra ref so we never clean up anyway.
	paths  map[string]*fileMmap
	closed bool
}

func (me *mmapFileIo) Close() error {
	me.mu.Lock()
	defer me.mu.Unlock()
	for name := range me.paths {
		me.closeName(name)
	}
	me.paths = nil
	me.closed = true
	return nil
}

func (me *mmapFileIo) closedErr() error {
	if me.closed {
		return fs.ErrClosed
	}
	return nil
}

func (me *mmapFileIo) rename(from, to string) (err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	me.closeName(from)
	me.closeName(to)
	return os.Rename(from, to)
}

func (me *mmapFileIo) closeName(name string) {
	v, ok := me.paths[name]
	if ok {
		// We're forcibly closing the handle. Leave the store's ref intact so we're the only one
		// that closes it, then delete it anyway. We must be holding the IO context lock to be doing
		// this if we're not using operation locks.
		panicif.Err(v.close())
		g.MustDelete(me.paths, name)
	}
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

// Shared file access.
type fileMmap struct {
	// Read lock held for each handle. Write lock taken for destructive action like close.
	mu       sync.RWMutex
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
	// I can't see any way to avoid this. We need to forcibly alter the actual state of the handle
	// underneath other consumers to kick them off. Additionally, we need to exclude users of its raw
	// file descriptor. This is a potential deadlock zone if handles have lifetimes that escape the
	// file storage implementation (like with NewReader, which don't provide for it).
	me.mu.Lock()
	defer me.mu.Unlock()
	// There's no double-close protection here. Not sure if that's an issue. Probably not since we
	// don't evict the store's reference anywhere for now.
	return errors.Join(me.m.Unmap(), me.f.Close())
}

func (me *fileMmap) inc() {
	panicif.LessThanOrEqual(me.refs.Add(1), 0)
}

func (me *mmapFileIo) openForSharedRead(name string) (_ sharableReader, err error) {
	return me.openReadOnly(name)
}

func (me *mmapFileIo) openForRead(name string) (_ fileReader, err error) {
	sh, err := me.openReadOnly(name)
	if err != nil {
		return
	}
	return &mmapFileHandle{
		shared: sh,
	}, nil
}

func (me *mmapFileIo) openReadOnly(name string) (_ *mmapSharedFileHandle, err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	err = me.closedErr()
	if err != nil {
		return
	}
	v, ok := me.paths[name]
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
	v = me.addNewMmap(name, mm, false, f)
	return newMmapFile(v), nil
}

func (me *mmapFileIo) openForWrite(name string, size int64) (_ fileWriter, err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	err = me.closedErr()
	if err != nil {
		return
	}
	v, ok := me.paths[name]
	if ok {
		if int64(len(v.m)) == size && v.writable {
			return newMmapFile(v), nil
		} else {
			// Drop the cache ref. We aren't presuming to require it to be closed here, hmm...
			v.dec()
			g.MustDelete(me.paths, name)
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
	return newMmapFile(me.addNewMmap(name, mm, true, f)), nil
}

func newMmapFile(f *fileMmap) *mmapSharedFileHandle {
	if !lockHandleOperations {
		// This can't fail because we have to be holding the IO context lock to be here.
		panicif.False(f.mu.TryRLock())
	}
	ret := &mmapSharedFileHandle{
		f: f,
		close: sync.OnceValue[error](func() error {
			if !lockHandleOperations {
				f.mu.RUnlock()
			}
			return f.dec()
		}),
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
	close func() error
}

func (me *mmapSharedFileHandle) WriteAt(p []byte, off int64) (n int, err error) {
	// It's not actually worth the hassle to write using mmap here since the caller provided the
	// buffer already.
	return me.f.f.WriteAt(p, off)
}

func (me *mmapSharedFileHandle) ReadAt(p []byte, off int64) (n int, err error) {
	n = copy(p, me.f.m[off:])
	if n < len(p) {
		if off < 0 {
			err = fs.ErrInvalid
			return
		}
	}
	if off+int64(n) == int64(len(me.f.m)) {
		err = io.EOF
	}
	return
}

func (me *mmapSharedFileHandle) Close() error {
	return me.close()
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
	mu := &me.shared.f.mu
	// If this panics we need a close error.
	if lockHandleOperations {
		mu.RLock()
	}
	b := me.shared.f.m
	panicif.Nil(b) // It's been closed and we need to signal that.
	if me.pos >= int64(len(b)) {
		return
	}
	b = b[me.pos:]
	b = b[:min(int64(len(b)), n)]
	i, err := w.Write(b)
	if lockHandleOperations {
		mu.RUnlock()
	}
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

	//  We do need to protect the file descriptor as that's not synchronized outside os.File. If
	//  it's already closed before we call this, that's fine, we'll get EBADF. Don't recursively
	//  RLock here if we're RLocking at the reference level.
	mu := &me.shared.f.mu
	if lockHandleOperations {
		mu.RLock()
	}
	ret, err = seekData(me.shared.f.f, offset)
	if lockHandleOperations {
		mu.RUnlock()
	}
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
