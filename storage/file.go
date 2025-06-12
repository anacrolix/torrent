package storage

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/james-lawrence/torrent/internal/langx"
	"github.com/james-lawrence/torrent/metainfo"
)

type FilePathMaker func(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, finfo *metainfo.FileInfo) string
type FileOption func(*fileClientImpl)

func FileOptionPathMaker(m FilePathMaker) FileOption {
	return func(fci *fileClientImpl) {
		fci.pathMaker = m
	}
}

func FileOptionPathMakerInfohash(fci *fileClientImpl) {
	fci.pathMaker = InfoHashPathMaker
}

func FileOptionPathMakerInfohashV0(fci *fileClientImpl) {
	fci.pathMaker = infoHashPathMakerV0
}

func FileOptionPathMakerFixed(s string) FileOption {
	return func(fci *fileClientImpl) {
		fci.pathMaker = fixedPathMaker(s)
	}
}

// File-based storage for torrents, that isn't yet bound to a particular
// torrent.
type fileClientImpl struct {
	baseDir   string
	pathMaker FilePathMaker
}

func fixedPathMaker(name string) FilePathMaker {
	return func(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
		s := filepath.Join(baseDir, name)
		return s
	}
}

func InfoHashPathMaker(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
	// s := filepath.Join(baseDir, infoHash.HexString(), langx.DefaultIfZero(langx.DerefOrZero(info).Name, filepath.Join(langx.DerefOrZero(fi).Path...)))
	s := filepath.Join(baseDir, infoHash.String(), filepath.Join(langx.DerefOrZero(fi).Path...))
	return s
}

func infoHashPathMakerV0(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
	return filepath.Join(baseDir, infoHash.String(), langx.DerefOrZero(info).Name, filepath.Join(langx.DerefOrZero(fi).Path...))
}

// All Torrent data stored in this baseDir
func NewFile(baseDir string, options ...FileOption) *fileClientImpl {
	return langx.Autoptr(langx.Clone(fileClientImpl{
		baseDir:   baseDir,
		pathMaker: InfoHashPathMaker,
	}, options...))
}

func (me *fileClientImpl) Close() error {
	return nil
}

func (fs *fileClientImpl) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	err := CreateNativeZeroLengthFiles(fs.baseDir, infoHash, info, fs.pathMaker)
	if err != nil {
		return nil, err
	}
	return &fileTorrentImpl{
		dir:       fs.baseDir,
		info:      info,
		infoHash:  infoHash,
		pathMaker: fs.pathMaker,
	}, nil
}

type fileTorrentImpl struct {
	closed    atomic.Bool
	dir       string
	info      *metainfo.Info
	infoHash  metainfo.Hash
	pathMaker FilePathMaker
}

// ReadAt implements TorrentImpl.
func (fts *fileTorrentImpl) ReadAt(p []byte, off int64) (n int, err error) {
	if fts.closed.Load() {
		return 0, ErrClosed()
	}
	return fileTorrentImplIO{fts}.ReadAt(p, off)
}

// WriteAt implements TorrentImpl.
func (fts *fileTorrentImpl) WriteAt(p []byte, off int64) (n int, err error) {
	if fts.closed.Load() {
		return 0, ErrClosed()
	}
	return fileTorrentImplIO{fts}.WriteAt(p, off)
}

func (fs *fileTorrentImpl) Close() error {
	fs.closed.Store(true)
	return nil
}

// Creates natives files for any zero-length file entries in the info. This is
// a helper for file-based storages, which don't address or write to zero-
// length files because they have no corresponding pieces.
func CreateNativeZeroLengthFiles(dir string, infohash metainfo.Hash, info *metainfo.Info, pathMaker FilePathMaker) (err error) {
	for _, fi := range info.UpvertedFiles() {
		if fi.Length != 0 {
			continue
		}

		name := pathMaker(dir, infohash, info, &fi)
		if err := os.MkdirAll(filepath.Dir(name), 0777); err != nil {
			return err
		}

		var f io.Closer
		if f, err = os.OpenFile(name, os.O_RDONLY|os.O_CREATE, 0600); err != nil {
			return err
		}

		f.Close()
	}

	return nil
}

type ioreadatclose interface {
	io.ReaderAt
	io.Closer
}

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type fileTorrentImplIO struct {
	fts *fileTorrentImpl
}

func (t *fileTorrentImplIO) reader(path string) (ioreadatclose, error) {
	if f, err := os.Open(path); err == nil {
		return f, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return nil, io.ErrUnexpectedEOF
}

// Returns EOF on short or missing file.
func (t *fileTorrentImplIO) readFileAt(fi metainfo.FileInfo, b []byte, off int64) (n int, err error) {
	filepath := t.fts.pathMaker(t.fts.dir, t.fts.infoHash, t.fts.info, &fi)

	f, err := t.reader(filepath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	// Limit the read to within the expected bounds of this file.
	if int64(len(b)) > fi.Length-off {
		b = b[:fi.Length-off]
	}

	return io.ReadFull(io.NewSectionReader(f, off, int64(len(b))), b)
}

// Only returns EOF at the end of the torrent. Premature EOF is ErrUnexpectedEOF.
func (t fileTorrentImplIO) ReadAt(b []byte, off int64) (n int, err error) {
	for _, fi := range t.fts.info.UpvertedFiles() {
		for off < fi.Length {
			n1, err1 := t.readFileAt(fi, b, off)
			n += n1
			off += int64(n1)
			b = b[n1:]
			if len(b) == 0 {
				// Got what we need.
				return
			}
			if n1 != 0 {
				// Made progress.
				continue
			}
			err = err1
			if err == io.EOF {
				// Lies.
				err = io.ErrUnexpectedEOF
			}
			return n, err
		}
		off -= fi.Length
	}

	return n, io.EOF
}

func (t fileTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	for _, fi := range t.fts.info.UpvertedFiles() {
		if off >= fi.Length {
			off -= fi.Length
			continue
		}
		n1 := len(p)
		if int64(n1) > fi.Length-off {
			n1 = int(fi.Length - off)
		}
		name := t.fts.pathMaker(t.fts.dir, t.fts.infoHash, t.fts.info, &fi)
		os.MkdirAll(filepath.Dir(name), 0777)
		var f *os.File
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			return
		}

		n1, err = f.WriteAt(p[:n1], off)
		// TODO: On some systems, write errors can be delayed until the Close.
		f.Close()
		if err != nil {
			return
		}
		n += n1
		off = 0
		p = p[n1:]
		if len(p) == 0 {
			break
		}
	}
	return
}
