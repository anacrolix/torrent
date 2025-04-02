package storage

import (
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/james-lawrence/torrent/metainfo"
)

// File-based storage for torrents, that isn't yet bound to a particular
// torrent.
type fileClientImpl struct {
	baseDir   string
	pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string
}

// The Default path maker just returns the current path
func defaultPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return baseDir
}

func infoHashPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return filepath.Join(baseDir, infoHash.HexString())
}

// All Torrent data stored in this baseDir
func NewFile(baseDir string) ClientImpl {
	return newFileWithCustomPathMakerAndCompletion(baseDir, nil)
}

// File storage with data partitioned by infohash.
func NewFileByInfoHash(baseDir string) ClientImpl {
	return NewFileWithCustomPathMaker(baseDir, infoHashPathMaker)
}

// Allows passing a function to determine the path for storing torrent data
func NewFileWithCustomPathMaker(baseDir string, pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string) ClientImpl {
	return newFileWithCustomPathMakerAndCompletion(baseDir, pathMaker)
}

func newFileWithCustomPathMakerAndCompletion(baseDir string, pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string) ClientImpl {
	if pathMaker == nil {
		pathMaker = defaultPathMaker
	}
	return &fileClientImpl{
		baseDir:   baseDir,
		pathMaker: pathMaker,
	}
}

func (me *fileClientImpl) Close() error {
	return nil
}

func (fs *fileClientImpl) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	dir := fs.pathMaker(fs.baseDir, info, infoHash)
	err := CreateNativeZeroLengthFiles(info, dir)
	if err != nil {
		return nil, err
	}
	return &fileTorrentImpl{
		dir:      dir,
		info:     info,
		infoHash: infoHash,
	}, nil
}

type fileTorrentImpl struct {
	closed   atomic.Bool
	dir      string
	info     *metainfo.Info
	infoHash metainfo.Hash
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
func CreateNativeZeroLengthFiles(info *metainfo.Info, dir string) (err error) {
	for _, fi := range info.UpvertedFiles() {
		if fi.Length != 0 {
			continue
		}
		name := filepath.Join(append([]string{dir, info.Name}, fi.Path...)...)
		os.MkdirAll(filepath.Dir(name), 0777)
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
	// log.Println("reading file", fst.fileInfoName(fi))
	f, err := os.Open(fst.fileInfoName(fi))
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

func (fst *fileTorrentImplIO) fileInfoName(fi metainfo.FileInfo) string {
	return filepath.Join(append([]string{fst.fts.dir, fst.fts.info.Name}, fi.Path...)...)
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
		name := fst.fileInfoName(fi)
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
