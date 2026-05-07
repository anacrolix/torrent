package storage

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
)

type classicFileIo struct {
	mu      sync.Mutex
	writers map[string]*os.File
	closed  bool
}

func newClassicFileIo() fileIo {
	return &classicFileIo{}
}

func (me *classicFileIo) Close() error {
	me.mu.Lock()
	defer me.mu.Unlock()
	var err error
	for name, f := range me.writers {
		err = errors.Join(err, f.Close())
		delete(me.writers, name)
	}
	me.closed = true
	return err
}

func (me *classicFileIo) rename(from, to string) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	if err := me.closedErr(); err != nil {
		return err
	}
	err := me.closeWriterLocked(from)
	err = errors.Join(err, me.closeWriterLocked(to))
	err = errors.Join(err, os.Rename(from, to))
	return err
}

func (me *classicFileIo) flush(name string, offset, nbytes int64) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	if err := me.closedErr(); err != nil {
		return err
	}
	if f, ok := me.writers[name]; ok {
		return f.Sync()
	}
	return fsync(name)
}

func (me *classicFileIo) openForSharedRead(name string) (sharableReader, error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if err := me.closedErr(); err != nil {
		return nil, err
	}
	return os.Open(name)
}

func (me *classicFileIo) openForRead(name string) (fileReader, error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if err := me.closedErr(); err != nil {
		return nil, err
	}
	if writer, ok := me.writers[name]; ok {
		return &classicBorrowedFileReader{f: writer}, nil
	}
	f, err := os.Open(name)
	return classicFileReader{f}, err
}

func (me *classicFileIo) openForWrite(p string, size int64) (f fileWriter, err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if err := me.closedErr(); err != nil {
		return nil, err
	}
	if cached, ok := me.writers[p]; ok {
		return classicFileWriter{cached}, nil
	}
	cached, err := openFileExtra(p, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if me.writers == nil {
		me.writers = make(map[string]*os.File)
	}
	me.writers[p] = cached
	return classicFileWriter{cached}, nil
}

func (me *classicFileIo) closeWriterLocked(name string) error {
	if me.writers == nil {
		return nil
	}
	f, ok := me.writers[name]
	if !ok {
		return nil
	}
	delete(me.writers, name)
	return f.Close()
}

func (me *classicFileIo) closedErr() error {
	if me.closed {
		return fs.ErrClosed
	}
	return nil
}

type classicFileWriter struct {
	*os.File
}

func (classicFileWriter) Close() error {
	return nil
}

type classicFileReader struct {
	*os.File
}

func (c classicFileReader) writeToN(w io.Writer, n int64) (written int64, err error) {
	return io.CopyN(w, c.File, n)
}

func (c classicFileReader) seekDataOrEof(offset int64) (ret int64, err error) {
	ret, err = seekData(c.File, offset)
	if err == io.EOF {
		ret, err = c.File.Seek(0, io.SeekEnd)
	}
	return
}

// Reuses the open writer handle to avoid an extra open during piece hashing.
// This is intentionally only used for openForRead, not shared readers, so the
// borrowed handle lifetime stays scoped to storage-internal operations.
type classicBorrowedFileReader struct {
	f   *os.File
	pos int64
}

func (c *classicBorrowedFileReader) Close() error {
	return nil
}

func (c *classicBorrowedFileReader) Read(p []byte) (n int, err error) {
	n, err = c.f.ReadAt(p, c.pos)
	c.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return
}

func (c *classicBorrowedFileReader) writeToN(w io.Writer, n int64) (written int64, err error) {
	written, err = io.Copy(w, io.NewSectionReader(c.f, c.pos, n))
	c.pos += written
	return
}

func (c *classicBorrowedFileReader) seekDataOrEof(offset int64) (ret int64, err error) {
	ret, err = seekData(c.f, offset)
	if err == io.EOF {
		var fi os.FileInfo
		fi, err = c.f.Stat()
		if err != nil {
			return c.pos, err
		}
		ret = fi.Size()
		err = nil
	}
	if err != nil {
		return c.pos, err
	}
	c.pos = ret
	return
}
