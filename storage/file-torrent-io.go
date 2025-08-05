package storage

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/segments"
)

// Exposes file-based storage of a torrent, as one big ReadWriterAt.
type fileTorrentImplIO struct {
	fts *fileTorrentImpl
}

// Returns EOF on short or missing file.
func (fst fileTorrentImplIO) readFileAt(file file, b []byte, off int64) (n int, err error) {
	fst.fts.logger().Debug("readFileAt", "file.safeOsPath", file.safeOsPath)
	f, err := fst.fts.openSharedFile(file)
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
	if int64(len(b)) > file.length()-off {
		b = b[:file.length()-off]
	}
	for off < file.length() && len(b) != 0 {
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
	for i, e := range fst.fts.segmentLocater.LocateIter(
		segments.Extent{off, int64(len(b))},
	) {
		n1, err1 := fst.readFileAt(fst.fts.file(i), b[:e.Length], e.Start)
		n += n1
		b = b[n1:]
		if segments.Int(n1) == e.Length {
			switch err1 {
			// ReaderAt.ReadAt contract.
			case nil, io.EOF:
			default:
				err = err1
				return
			}
		} else {
			panicif.Nil(err1)
			err = err1
			return
		}
	}
	if len(b) != 0 {
		// We're at the end of the torrent.
		err = io.EOF
	}
	return
}

func (fst fileTorrentImplIO) openForWrite(file file) (f *os.File, err error) {
	// It might be possible to have a writable handle shared files cache if we need it.
	fst.fts.logger().Debug("openForWrite", "file.safeOsPath", file.safeOsPath)
	p := fst.fts.pathForWrite(&file)
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
	} else {
		return
	}
	f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE, filePerm)
	return
}

func (fst fileTorrentImplIO) WriteAt(p []byte, off int64) (n int, err error) {
	for i, e := range fst.fts.segmentLocater.LocateIter(
		segments.Extent{off, int64(len(p))},
	) {
		var f *os.File
		f, err = fst.openForWrite(fst.fts.file(i))
		if err != nil {
			return
		}
		var n1 int
		n1, err = f.WriteAt(p[:e.Length], e.Start)
		closeErr := f.Close()
		n += n1
		p = p[n1:]
		if err == nil {
			err = closeErr
		}
		if err == nil && int64(n1) != e.Length {
			err = io.ErrShortWrite
		}
		if err != nil {
			return
		}
	}
	return
}
