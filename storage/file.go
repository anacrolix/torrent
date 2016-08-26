package storage

import (
	"io"
	"os"
	"path/filepath"

	"github.com/anacrolix/missinggo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/anacrolix/torrent/metainfo"
)

// File-based storage for torrents, that isn't yet bound to a particular
// torrent.
type fileStorage struct {
	baseDir string
}

func NewFile(baseDir string) Client {
	return &fileStorage{
		baseDir: baseDir,
	}
}

func (fs *fileStorage) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (Torrent, error) {
	return &fileTorrentStorage{
		fs,
		info,
		infoHash,
		pieceCompletionForDir(fs.baseDir),
	}, nil
}

// File-based torrent storage, not yet bound to a Torrent.
type fileTorrentStorage struct {
	fs         *fileStorage
	info       *metainfo.Info
	infoHash   metainfo.Hash
	completion pieceCompletion
}

func (fts *fileTorrentStorage) Piece(p metainfo.Piece) Piece {
	// Create a view onto the file-based torrent storage.
	_io := fileStorageTorrent{fts}
	// Return the appropriate segments of this.
	return &fileStoragePiece{
		fts,
		p,
		missinggo.NewSectionWriter(_io, p.Offset(), p.Length()),
		io.NewSectionReader(_io, p.Offset(), p.Length()),
	}
}

func (fs *fileTorrentStorage) Close() error {
	fs.completion.Close()
	return nil
}

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type fileStorageTorrent struct {
	fts *fileTorrentStorage
}

// Returns EOF on short or missing file.
func (fst *fileStorageTorrent) readFileAt(fi metainfo.FileInfo, b []byte, off int64) (n int, err error) {
	f, err := os.Open(fst.fts.fileInfoName(fi))
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
func (fst fileStorageTorrent) ReadAt(b []byte, off int64) (n int, err error) {
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
			return
		}
		off -= fi.Length
	}
	err = io.EOF
	return
}

func (fst fileStorageTorrent) WriteAt(p []byte, off int64) (n int, err error) {
	for _, fi := range fst.fts.info.UpvertedFiles() {
		if off >= fi.Length {
			off -= fi.Length
			continue
		}
		n1 := len(p)
		if int64(n1) > fi.Length-off {
			n1 = int(fi.Length - off)
		}
		name := fst.fts.fileInfoName(fi)
		os.MkdirAll(filepath.Dir(name), 0770)
		var f *os.File
		f, err = os.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0660)
		if err != nil {
			return
		}
		n1, err = f.WriteAt(p[:n1], off)
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

func (fts *fileTorrentStorage) fileInfoName(fi metainfo.FileInfo) string {
	return filepath.Join(append([]string{fts.fs.baseDir, fts.info.Name}, fi.Path...)...)
}
