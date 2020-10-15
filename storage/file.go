package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/segments"

	"github.com/anacrolix/torrent/metainfo"
)

// File-based storage for torrents, that isn't yet bound to a particular
// torrent.
type fileClientImpl struct {
	baseDir   string
	pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string
	pc        PieceCompletion
}

// The Default path maker just returns the current path
func defaultPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return baseDir
}

func infoHashPathMaker(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
	return filepath.Join(baseDir, infoHash.HexString())
}

// All Torrent data stored in this baseDir
func NewFile(baseDir string) ClientImplCloser {
	return NewFileWithCompletion(baseDir, pieceCompletionForDir(baseDir))
}

func NewFileWithCompletion(baseDir string, completion PieceCompletion) *fileClientImpl {
	return newFileWithCustomPathMakerAndCompletion(baseDir, nil, completion)
}

// File storage with data partitioned by infohash.
func NewFileByInfoHash(baseDir string) ClientImpl {
	return NewFileWithCustomPathMaker(baseDir, infoHashPathMaker)
}

// Allows passing a function to determine the path for storing torrent data. The function is
// responsible for sanitizing the info if it uses some part of it (for example sanitizing
// info.Name).
func NewFileWithCustomPathMaker(baseDir string, pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string) ClientImpl {
	return newFileWithCustomPathMakerAndCompletion(baseDir, pathMaker, pieceCompletionForDir(baseDir))
}

func newFileWithCustomPathMakerAndCompletion(baseDir string, pathMaker func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string, completion PieceCompletion) *fileClientImpl {
	if pathMaker == nil {
		pathMaker = defaultPathMaker
	}
	return &fileClientImpl{
		baseDir:   baseDir,
		pathMaker: pathMaker,
		pc:        completion,
	}
}

func (me *fileClientImpl) Close() error {
	return me.pc.Close()
}

func (fs *fileClientImpl) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error) {
	dir := fs.pathMaker(fs.baseDir, info, infoHash)
	upvertedFiles := info.UpvertedFiles()
	files := make([]file, 0, len(upvertedFiles))
	for i, fileInfo := range upvertedFiles {
		s, err := ToSafeFilePath(append([]string{info.Name}, fileInfo.Path...)...)
		if err != nil {
			return nil, fmt.Errorf("file %v has unsafe path %q: %w", i, fileInfo.Path, err)
		}
		f := file{
			path:   filepath.Join(dir, s),
			length: fileInfo.Length,
		}
		if f.length == 0 {
			err = CreateNativeZeroLengthFile(f.path)
			if err != nil {
				return nil, fmt.Errorf("creating zero length file: %w", err)
			}
		}
		files = append(files, f)
	}
	return &fileTorrentImpl{
		files,
		segments.NewIndex(common.LengthIterFromUpvertedFiles(upvertedFiles)),
		infoHash,
		fs.pc,
	}, nil
}

type file struct {
	// The safe, OS-local file path.
	path   string
	length int64
}

type fileTorrentImpl struct {
	files          []file
	segmentLocater segments.Index
	infoHash       metainfo.Hash
	completion     PieceCompletion
}

func (fts *fileTorrentImpl) Piece(p metainfo.Piece) PieceImpl {
	// Create a view onto the file-based torrent storage.
	_io := fileTorrentImplIO{fts}
	// Return the appropriate segments of this.
	return &filePieceImpl{
		fts,
		p,
		missinggo.NewSectionWriter(_io, p.Offset(), p.Length()),
		io.NewSectionReader(_io, p.Offset(), p.Length()),
	}
}

func (fs *fileTorrentImpl) Close() error {
	return nil
}

// A helper to create zero-length files which won't appear for file-orientated storage since no
// writes will ever occur to them (no torrent data is associated with a zero-length file). The
// caller should make sure the file name provided is safe/sanitized.
func CreateNativeZeroLengthFile(name string) error {
	os.MkdirAll(filepath.Dir(name), 0777)
	var f io.Closer
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	return f.Close()
}

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type fileTorrentImplIO struct {
	fts *fileTorrentImpl
}

// Returns EOF on short or missing file.
func (fst *fileTorrentImplIO) readFileAt(file file, b []byte, off int64) (n int, err error) {
	f, err := os.Open(file.path)
	if os.IsNotExist(err) {
		// File missing is treated the same as a short file.
		err = io.EOF
		return
	}
	if err != nil {
		return
	}
	defer f.Close()
	// Limit the read to within the expected bounds of this file.
	if int64(len(b)) > file.length-off {
		b = b[:file.length-off]
	}
	for off < file.length && len(b) != 0 {
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
	fst.fts.segmentLocater.Locate(segments.Extent{off, int64(len(b))}, func(i int, e segments.Extent) bool {
		n1, err1 := fst.readFileAt(fst.fts.files[i], b[:e.Length], e.Start)
		n += n1
		b = b[n1:]
		err = err1
		return err == nil // && int64(n1) == e.Length
	})
	if len(b) != 0 && err == nil {
		err = io.EOF
	}
	return
}

func (fst fileTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	//log.Printf("write at %v: %v bytes", off, len(p))
	fst.fts.segmentLocater.Locate(segments.Extent{off, int64(len(p))}, func(i int, e segments.Extent) bool {
		name := fst.fts.files[i].path
		os.MkdirAll(filepath.Dir(name), 0777)
		var f *os.File
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			return false
		}
		var n1 int
		n1, err = f.WriteAt(p[:e.Length], e.Start)
		//log.Printf("%v %v wrote %v: %v", i, e, n1, err)
		closeErr := f.Close()
		n += n1
		p = p[n1:]
		if err == nil {
			err = closeErr
		}
		if err == nil && int64(n1) != e.Length {
			err = io.ErrShortWrite
		}
		return err == nil
	})
	return
}
