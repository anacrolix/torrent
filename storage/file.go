package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"os"
	"path/filepath"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
)

// File-based storage for torrents, that isn't yet bound to a particular torrent.
type fileClientImpl struct {
	opts NewFileClientOpts
}

// All Torrent data stored in this baseDir. The info names of each torrent are used as directories.
func NewFile(baseDir string) ClientImplCloser {
	return NewFileWithCompletion(baseDir, pieceCompletionForDir(baseDir))
}

type NewFileClientOpts struct {
	// The base directory for all downloads.
	ClientBaseDir   string
	FilePathMaker   FilePathMaker
	TorrentDirMaker TorrentDirFilePathMaker
	PieceCompletion PieceCompletion
	UsePartFiles    g.Option[bool]
	Logger          *slog.Logger
}

// NewFileOpts creates a new ClientImplCloser that stores files using the OS native filesystem.
func NewFileOpts(opts NewFileClientOpts) ClientImplCloser {
	if opts.TorrentDirMaker == nil {
		opts.TorrentDirMaker = defaultPathMaker
	}
	if opts.FilePathMaker == nil {
		opts.FilePathMaker = func(opts FilePathMakerOpts) string {
			var parts []string
			if opts.Info.BestName() != metainfo.NoName {
				parts = append(parts, opts.Info.BestName())
			}
			return filepath.Join(append(parts, opts.File.BestPath()...)...)
		}
	}
	if opts.PieceCompletion == nil {
		opts.PieceCompletion = pieceCompletionForDir(opts.ClientBaseDir)
	}
	if opts.Logger == nil {
		opts.Logger = log.Default.Slogger()
	}
	return &fileClientImpl{opts}
}

func (me *fileClientImpl) Close() error {
	return me.opts.PieceCompletion.Close()
}

func enumIter[T any](i iter.Seq[T]) iter.Seq2[int, T] {
	return func(yield func(int, T) bool) {
		j := 0
		for t := range i {
			if !yield(j, t) {
				return
			}
			j++
		}
	}
}

func (fs *fileClientImpl) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (_ TorrentImpl, err error) {
	dir := fs.opts.TorrentDirMaker(fs.opts.ClientBaseDir, info, infoHash)
	logger := log.ContextLogger(ctx).Slogger()
	logger.DebugContext(ctx, "opened file torrent storage", slog.String("dir", dir))
	var files []file
	for i, fileInfo := range enumIter(info.UpvertedFilesIter()) {
		filePath := filepath.Join(dir, fs.opts.FilePathMaker(FilePathMakerOpts{
			Info: info,
			File: &fileInfo,
		}))
		if !isSubFilepath(dir, filePath) {
			err = fmt.Errorf("file %v: path %q is not sub path of %q", i, filePath, dir)
			return
		}
		f := file{
			safeOsPath:      filePath,
			length:          fileInfo.Length,
			beginPieceIndex: fileInfo.BeginPieceIndex(info.PieceLength),
			endPieceIndex:   fileInfo.EndPieceIndex(info.PieceLength),
		}
		if f.length == 0 {
			err = CreateNativeZeroLengthFile(f.safeOsPath)
			if err != nil {
				err = fmt.Errorf("creating zero length file: %w", err)
				return
			}
		}
		files = append(files, f)
	}
	t := &fileTorrentImpl{
		info,
		files,
		info.FileSegmentsIndex(),
		infoHash,
		fs,
	}
	return TorrentImpl{
		Piece: t.Piece,
		Close: t.Close,
		Flush: t.Flush,
	}, nil
}

type file struct {
	// The safe, OS-local file path.
	safeOsPath      string
	beginPieceIndex int
	endPieceIndex   int
	length          int64
}

func (f file) partFilePath() string {
	return f.safeOsPath + ".part"
}

type fileTorrentImpl struct {
	info           *metainfo.Info
	files          []file
	segmentLocater segments.Index
	infoHash       metainfo.Hash
	// Save memory by pointing to the other data.
	client *fileClientImpl
}

func (fts *fileTorrentImpl) partFiles() bool {
	return fts.client.opts.UsePartFiles.UnwrapOr(true)
}

func (fts *fileTorrentImpl) pathForWrite(f file) string {
	if fts.partFiles() {
		return f.partFilePath()
	}
	return f.safeOsPath
}

func (fts *fileTorrentImpl) getCompletion(piece int) Completion {
	cmpl, err := fts.client.opts.PieceCompletion.Get(metainfo.PieceKey{
		fts.infoHash, piece,
	})
	cmpl.Err = errors.Join(cmpl.Err, err)
	return cmpl
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

func fsync(filePath string) (err error) {
	f, err := os.OpenFile(filePath, os.O_WRONLY, filePerm)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func (fts *fileTorrentImpl) Flush() error {
	for _, f := range fts.files {
		if err := fsync(fts.pathForWrite(f)); err != nil {
			return err
		}
	}
	return nil
}

// A helper to create zero-length files which won't appear for file-orientated storage since no
// writes will ever occur to them (no torrent data is associated with a zero-length file). The
// caller should make sure the file name provided is safe/sanitized.
func CreateNativeZeroLengthFile(name string) error {
	os.MkdirAll(filepath.Dir(name), dirPerm)
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
func (fst fileTorrentImplIO) readFileAt(file file, b []byte, off int64) (n int, err error) {
	var f *os.File
	if fst.fts.partFiles() {
		f, err = os.Open(file.partFilePath())
	}
	if err == nil && f == nil || errors.Is(err, fs.ErrNotExist) {
		f, err = os.Open(file.safeOsPath)
	}
	if errors.Is(err, fs.ErrNotExist) {
		// File missing is treated the same as a short file. Should we propagate this through the
		// interface now that fs.ErrNotExist is a thing?
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

func (fst fileTorrentImplIO) openForWrite(file file) (f *os.File, err error) {
	p := fst.fts.pathForWrite(file)
	f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE, filePerm)
	if err == nil {
		return
	}
	if errors.Is(err, fs.ErrNotExist) {
		err = os.MkdirAll(filepath.Dir(p), dirPerm)
		if err != nil {
			return
		}
	} else if errors.Is(err, fs.ErrPermission) {
		err = os.Chmod(p, filePerm)
		if err != nil {
			return
		}
	}
	f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE, filePerm)
	return
}

func (fst fileTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	// log.Printf("write at %v: %v bytes", off, len(p))
	fst.fts.segmentLocater.Locate(segments.Extent{off, int64(len(p))}, func(i int, e segments.Extent) bool {
		var f *os.File
		f, err = fst.openForWrite(fst.fts.files[i])
		if err != nil {
			return false
		}
		var n1 int
		n1, err = f.WriteAt(p[:e.Length], e.Start)
		// log.Printf("%v %v wrote %v: %v", i, e, n1, err)
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
