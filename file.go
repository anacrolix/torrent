package torrent

import (
	"strings"

	"github.com/james-lawrence/torrent/metainfo"
)

// File provides access to regions of torrent data that correspond to its files.
type File struct {
	t      *torrent
	path   string
	offset int64
	length int64
	fi     metainfo.FileInfo
}

// Torrent returns the associated torrent
func (f *File) Torrent() Torrent {
	return f.t
}

// Offset data for this file begins this many bytes into the Torrent.
func (f *File) Offset() int64 {
	return f.offset
}

// FileInfo from the metainfo.Info to which this file corresponds.
func (f File) FileInfo() metainfo.FileInfo {
	return f.fi
}

// Path the file's path components joined by '/'.
func (f File) Path() string {
	return f.path
}

// Length the file's length in bytes.
func (f *File) Length() int64 {
	return f.length
}

// BytesCompleted number of bytes of the entire file we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces.
func (f *File) BytesCompleted() int64 {
	f.t.rLock()
	defer f.t.rUnlock()
	return f.bytesCompleted()
}

func (f *File) bytesCompleted() int64 {
	return f.length - f.bytesLeft()
}

func (f *File) bytesLeft() (left int64) {
	pieceSize := int64(f.t.usualPieceSize())
	firstPieceIndex := f.firstPieceIndex()
	endPieceIndex := f.endPieceIndex() - 1

	dup := f.t.chunks.completed.Clone()
	dup.Flip(firstPieceIndex+1, endPieceIndex)
	dup.Iterate(func(piece uint32) bool {
		if uint64(piece) >= endPieceIndex {
			return false
		}
		if uint64(piece) > firstPieceIndex {
			left += pieceSize
		}
		return true
	})

	if !f.t.chunks.ChunksComplete(uint64(firstPieceIndex)) {
		left += pieceSize - (f.offset % pieceSize)
	}

	if !f.t.chunks.ChunksComplete(uint64(endPieceIndex)) {
		left += (f.offset + f.length) % pieceSize
	}
	return
}

// DisplayPath the relative file path for a multi-file torrent, and the torrent name for a
// single-file torrent.
func (f *File) DisplayPath() string {
	fip := f.FileInfo().Path
	if len(fip) == 0 {
		return f.t.info.Name
	}
	return strings.Join(fip, "/")

}

func byteRegionExclusivePieces(off, size, pieceSize int64) (begin, end int) {
	begin = int((off + pieceSize - 1) / pieceSize)
	end = int((off + size) / pieceSize)
	return
}

// NewReader returns a reader for the file.
func (f *File) NewReader() Reader {
	tr := reader{
		ReaderAt: f.t.storage,
		offset:   f.Offset(),
		length:   f.Length(),
	}

	return &tr
}

// Returns the index of the first piece containing data for the file.
func (f *File) firstPieceIndex() uint64 {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return uint64(f.offset / int64(f.t.usualPieceSize()))
}

// Returns the index of the piece after the last one containing data for the file.
func (f *File) endPieceIndex() uint64 {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return uint64((f.offset + f.length + int64(f.t.usualPieceSize()) - 1) / int64(f.t.usualPieceSize()))
}
