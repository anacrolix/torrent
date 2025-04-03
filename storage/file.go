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
	fci.pathMaker = infoHashPathMaker
}

func FileOptionPathMakerInfohashV0(fci *fileClientImpl) {
	fci.pathMaker = infoHashPathMakerV0
}

// File-based storage for torrents, that isn't yet bound to a particular
// torrent.
type fileClientImpl struct {
	baseDir   string
	pathMaker FilePathMaker
}

// The Default path maker just returns the current path
func defaultPathMaker(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
	return filepath.Join(baseDir, info.Name, filepath.Join(langx.DerefOrZero(fi).Path...))
}

func infoHashPathMaker(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
	return filepath.Join(baseDir, infoHash.HexString(), filepath.Join(langx.DerefOrZero(fi).Path...))
}

func infoHashPathMakerV0(baseDir string, infoHash metainfo.Hash, info *metainfo.Info, fi *metainfo.FileInfo) string {
	return filepath.Join(baseDir, infoHash.HexString(), info.Name, filepath.Join(langx.DerefOrZero(fi).Path...))
}

// All Torrent data stored in this baseDir
func NewFile(baseDir string, options ...FileOption) *fileClientImpl {
	return langx.Autoptr(langx.Clone(fileClientImpl{
		baseDir:   baseDir,
		pathMaker: defaultPathMaker,
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
	// log.Println("file storage ReadAt len", len(p), "offset", off)
	return fileTorrentImplIO{fts}.ReadAt(p, off)
}

// WriteAt implements TorrentImpl.
func (fts *fileTorrentImpl) WriteAt(p []byte, off int64) (n int, err error) {
	if fts.closed.Load() {
		return 0, ErrClosed()
	}
	// log.Printf("file storage WriteAt %p len %d offset %d\n", fts.mu, len(p), off)
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

		if f, err = os.Create(name); err != nil {
			return err
		}

		f.Close()
	}

	return nil
}

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type fileTorrentImplIO struct {
	fts *fileTorrentImpl
}

// Returns EOF on short or missing file.
func (fst *fileTorrentImplIO) readFileAt(fi metainfo.FileInfo, b []byte, off int64) (n int, err error) {
	filepath := fst.fts.pathMaker(fst.fts.dir, fst.fts.infoHash, fst.fts.info, &fi)
	// log.Println("reading file", filepath, off)
	f, err := os.Open(filepath)
	if os.IsNotExist(err) {
		// File missing is treated the same as a short file.
		return 0, io.EOF
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// Limit the read to within the expected bounds of this file.
	if int64(len(b)) > fi.Length-off {
		b = b[:fi.Length-off]
	}
	for off < fi.Length && len(b) != 0 {
		n1, err1 := f.ReadAt(b, off)
		b = b[n1:]
		n += n1
		off += int64(n1)
		if n1 == 0 {
			err = err1
			break
		}
	}
	return
}

// Only returns EOF at the end of the torrent. Premature EOF is ErrUnexpectedEOF.
func (fst fileTorrentImplIO) ReadAt(b []byte, off int64) (n int, err error) {
	for _, fi := range fst.fts.info.UpvertedFiles() {
		for off < fi.Length {
			n1, err1 := fst.readFileAt(fi, b, off)
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

func (fst fileTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	for _, fi := range fst.fts.info.UpvertedFiles() {
		if off >= fi.Length {
			off -= fi.Length
			continue
		}
		n1 := len(p)
		if int64(n1) > fi.Length-off {
			n1 = int(fi.Length - off)
		}
		name := fst.fts.pathMaker(fst.fts.dir, fst.fts.infoHash, fst.fts.info, &fi)
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
