package torrent

import (
	"strings"

	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/torrent/metainfo"
)

// Provides access to regions of torrent data that correspond to its files.
type File struct {
	t      *Torrent
	path   string
	offset int64
	length int64
	fi     metainfo.FileInfo
	prio   piecePriority
}

func (f *File) Torrent() *Torrent {
	return f.t
}

// Data for this file begins this many bytes into the Torrent.
func (f *File) Offset() int64 {
	return f.offset
}

// The FileInfo from the metainfo.Info to which this file corresponds.
func (f File) FileInfo() metainfo.FileInfo {
	return f.fi
}

// The file's path components joined by '/'.
func (f File) Path() string {
	return f.path
}

// The file's length in bytes.
func (f *File) Length() int64 {
	return f.length
}

// Number of bytes of the entire file we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces.
func (f *File) BytesCompleted() int64 {
	f.t.cl.rLock()
	defer f.t.cl.rUnlock()
	return f.bytesCompleted()
}

func (f *File) bytesCompleted() int64 {
	return f.length - f.bytesLeft()
}

func (f *File) bytesLeft() (left int64) {
	pieceSize := int64(f.t.usualPieceSize())
	firstPieceIndex := f.firstPieceIndex()
	endPieceIndex := f.endPieceIndex() - 1
	bitmap.Flip(f.t._completedPieces, firstPieceIndex+1, endPieceIndex).IterTyped(func(piece int) bool {
		if piece >= endPieceIndex {
			return false
		}
		if piece > firstPieceIndex {
			left += pieceSize
		}
		return true
	})
	if !f.t.pieceComplete(firstPieceIndex) {
		left += pieceSize - (f.offset % pieceSize)
	}
	if !f.t.pieceComplete(endPieceIndex) {
		left += (f.offset + f.length) % pieceSize
	}
	return
}

// The relative file path for a multi-file torrent, and the torrent name for a
// single-file torrent.
func (f *File) DisplayPath() string {
	fip := f.FileInfo().Path
	if len(fip) == 0 {
		return f.t.info.Name
	}
	return strings.Join(fip, "/")

}

// The download status of a piece that comprises part of a File.
type FilePieceState struct {
	Bytes int64 // Bytes within the piece that are part of this File.
	PieceState
}

// Returns the state of pieces in this file.
func (f *File) State() (ret []FilePieceState) {
	f.t.cl.rLock()
	defer f.t.cl.rUnlock()
	pieceSize := int64(f.t.usualPieceSize())
	off := f.offset % pieceSize
	remaining := f.length
	for i := pieceIndex(f.offset / pieceSize); ; i++ {
		if remaining == 0 {
			break
		}
		len1 := pieceSize - off
		if len1 > remaining {
			len1 = remaining
		}
		ps := f.t.pieceState(i)
		ret = append(ret, FilePieceState{len1, ps})
		off = 0
		remaining -= len1
	}
	return
}

// Requests that all pieces containing data in the file be downloaded.
func (f *File) Download() {
	f.SetPriority(PiecePriorityNormal)
}

func byteRegionExclusivePieces(off, size, pieceSize int64) (begin, end int) {
	begin = int((off + pieceSize - 1) / pieceSize)
	end = int((off + size) / pieceSize)
	return
}

// Deprecated: Use File.SetPriority.
func (f *File) Cancel() {
	f.SetPriority(PiecePriorityNone)
}

func (f *File) NewReader() Reader {
	tr := reader{
		mu:        f.t.cl.locker(),
		t:         f.t,
		readahead: 5 * 1024 * 1024,
		offset:    f.Offset(),
		length:    f.Length(),
	}
	f.t.addReader(&tr)
	return &tr
}

// Sets the minimum priority for pieces in the File.
func (f *File) SetPriority(prio piecePriority) {
	f.t.cl.lock()
	defer f.t.cl.unlock()
	if prio == f.prio {
		return
	}
	f.prio = prio
	f.t.updatePiecePriorities(f.firstPieceIndex(), f.endPieceIndex())
}

// Returns the priority per File.SetPriority.
func (f *File) Priority() piecePriority {
	f.t.cl.lock()
	defer f.t.cl.unlock()
	return f.prio
}

// Returns the index of the first piece containing data for the file.
func (f *File) firstPieceIndex() pieceIndex {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex(f.offset / int64(f.t.usualPieceSize()))
}

// Returns the index of the piece after the last one containing data for the file.
func (f *File) endPieceIndex() pieceIndex {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex((f.offset + f.length + int64(f.t.usualPieceSize()) - 1) / int64(f.t.usualPieceSize()))
}
