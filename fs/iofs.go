//go:build !windows

package torrentfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
)

// IOFS returns a read-only io/fs.FS view of tfs. It is built on the same traversal and read
// helpers as the FUSE backends, so it can be used to access torrent data without FUSE (for example
// with http.FileServerFS), and to run the tfstest suite without mounting anything.
//
// Reads block until the data is available, the file is closed, or tfs is destroyed. Files
// implement io.ReaderAt and io.Seeker; directories implement fs.ReadDirFile.
func (tfs *TorrentFS) IOFS() fs.FS {
	return ioFS{tfs: tfs}
}

type ioFS struct {
	tfs *TorrentFS
}

var _ fs.FS = ioFS{}

const (
	ioDirMode  = fs.ModeDir | 0o555
	ioFileMode = 0o444
)

// ioNode is a resolved entry in an ioFS.
type ioNode struct {
	// Base name of the entry; "." for the root.
	name string
	// nil for the root.
	t *torrent.Torrent
	// Path within t. "" for a torrent's top-level directory.
	path string
	// nil for directories.
	file *torrent.File
}

func (n ioNode) isDir() bool {
	return n.file == nil
}

func (n ioNode) info() fs.FileInfo {
	return ioFileInfo{n}
}

// child looks up name in the directory parent.
func (me ioFS) child(parent ioNode, name string) (c ioNode, ok bool) {
	var l TorrentLookup
	if parent.t == nil {
		l, ok = RootLookup(me.tfs, name)
		// The torrent's top-level directory is the empty path within the torrent.
		l.Path = ""
	} else {
		l, ok = DirLookup(parent.t, parent.path, name)
	}
	if !ok {
		return
	}
	c = ioNode{name: name, t: l.Torrent, path: l.Path, file: l.File}
	return
}

func (me ioFS) lookup(name string) (n ioNode, err error) {
	if !fs.ValidPath(name) {
		err = fs.ErrInvalid
		return
	}
	n = ioNode{name: "."}
	if name == "." {
		return
	}
	for _, comp := range strings.Split(name, "/") {
		if !n.isDir() {
			err = syscall.ENOTDIR
			return
		}
		var ok bool
		n, ok = me.child(n, comp)
		if !ok {
			err = fs.ErrNotExist
			return
		}
	}
	return
}

// readDir returns the entries of the directory dir sorted by name.
func (me ioFS) readDir(dir ioNode) (entries []fs.DirEntry) {
	var des []DirEntry
	if dir.t == nil {
		des = RootEntries(me.tfs)
	} else {
		des = DirEntries(dir.t, dir.path)
	}
	for _, de := range des {
		// Look up each entry to get its size. It might have gone away since it was listed.
		c, ok := me.child(dir, de.Name)
		if !ok {
			continue
		}
		entries = append(entries, fs.FileInfoToDirEntry(c.info()))
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return
}

func (me ioFS) Open(name string) (f fs.File, err error) {
	n, err := me.lookup(name)
	if err != nil {
		err = &fs.PathError{Op: "open", Path: name, Err: err}
		return
	}
	if n.isDir() {
		f = &ioDir{fs: me, node: n, name: name}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f = &ioFile{tfs: me.tfs, node: n, name: name, ctx: ctx, cancel: cancel}
	return
}

type ioFileInfo struct {
	n ioNode
}

var _ fs.FileInfo = ioFileInfo{}

func (me ioFileInfo) Name() string {
	return me.n.name
}

func (me ioFileInfo) Size() int64 {
	if me.n.isDir() {
		return 0
	}
	return me.n.file.Length()
}

func (me ioFileInfo) Mode() fs.FileMode {
	if me.n.isDir() {
		return ioDirMode
	}
	return ioFileMode
}

func (me ioFileInfo) ModTime() time.Time {
	return time.Time{}
}

func (me ioFileInfo) IsDir() bool {
	return me.n.isDir()
}

func (me ioFileInfo) Sys() any {
	return nil
}

type ioDir struct {
	fs   ioFS
	node ioNode
	// The name passed to Open, for errors.
	name string
	// Entries not yet returned by ReadDir. Populated on the first ReadDir.
	entries []fs.DirEntry
	listed  bool
}

var _ fs.ReadDirFile = (*ioDir)(nil)

func (d *ioDir) Stat() (fs.FileInfo, error) {
	return d.node.info(), nil
}

func (d *ioDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: syscall.EISDIR}
}

func (d *ioDir) ReadDir(n int) (entries []fs.DirEntry, err error) {
	if !d.listed {
		d.entries = d.fs.readDir(d.node)
		d.listed = true
	}
	if n <= 0 {
		entries = d.entries
		d.entries = nil
		return
	}
	if len(d.entries) == 0 {
		err = io.EOF
		return
	}
	n = min(n, len(d.entries))
	entries = d.entries[:n:n]
	d.entries = d.entries[n:]
	return
}

func (d *ioDir) Close() error {
	return nil
}

type ioFile struct {
	tfs  *TorrentFS
	node ioNode
	// The name passed to Open, for errors.
	name string
	// Cancelled by Close, which aborts reads in progress.
	ctx    context.Context
	cancel context.CancelFunc
	off    int64
}

var _ interface {
	fs.File
	io.ReaderAt
	io.Seeker
} = (*ioFile)(nil)

func (f *ioFile) Stat() (fs.FileInfo, error) {
	return f.node.info(), nil
}

func (f *ioFile) Read(p []byte) (n int, err error) {
	n, err = f.ReadAt(p, f.off)
	f.off += int64(n)
	return
}

func (f *ioFile) ReadAt(p []byte, off int64) (n int, err error) {
	if f.ctx.Err() != nil {
		err = &fs.PathError{Op: "read", Path: f.name, Err: fs.ErrClosed}
		return
	}
	if off < 0 {
		err = &fs.PathError{Op: "read", Path: f.name, Err: errors.New("negative offset")}
		return
	}
	if len(p) == 0 {
		return
	}
	remaining := f.node.file.Length() - off
	if remaining <= 0 {
		err = io.EOF
		return
	}
	dest := p
	if int64(len(dest)) > remaining {
		dest = dest[:remaining]
	}
	n, err = ReadFile(f.ctx, f.tfs, f.node.file, dest, off)
	if err != nil {
		if f.ctx.Err() != nil {
			err = fs.ErrClosed
		}
		err = &fs.PathError{Op: "read", Path: f.name, Err: err}
		return
	}
	if n < len(p) {
		err = io.EOF
	}
	return
}

func (f *ioFile) Seek(offset int64, whence int) (ret int64, err error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = f.off + offset
	case io.SeekEnd:
		pos = f.node.file.Length() + offset
	default:
		err = &fs.PathError{Op: "seek", Path: f.name, Err: fs.ErrInvalid}
		return
	}
	if pos < 0 {
		err = &fs.PathError{Op: "seek", Path: f.name, Err: errors.New("negative position")}
		return
	}
	f.off = pos
	ret = pos
	return
}

// Close aborts any reads in progress on f.
func (f *ioFile) Close() error {
	f.cancel()
	return nil
}
