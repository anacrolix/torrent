package torrent

import (
	"crypto/sha256"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/torrent/metainfo"
)

// Provides access to regions of torrent data that correspond to its files.
type File struct {
	t           *Torrent
	path        string
	offset      int64
	length      int64
	fi          metainfo.FileInfo
	displayPath string
	prio        PiecePriority
	piecesRoot  g.Option[[sha256.Size]byte]
}

func (f *File) String() string {
	return f.Path()
}

func (f *File) Torrent() *Torrent {
	return f.t
}

// Data for this file begins this many bytes into the Torrent.
func (f *File) Offset() int64 {
	return f.offset
}

// The FileInfo from the metainfo.Info to which this file corresponds.
func (f *File) FileInfo() metainfo.FileInfo {
	return f.fi
}

// The file's path components joined by '/'.
func (f *File) Path() string {
	return f.path
}

// The file's length in bytes.
func (f *File) Length() int64 {
	return f.length
}

// Number of bytes of the entire file we have completed. This is the sum of
// completed pieces, and dirtied chunks of incomplete pieces.
func (f *File) BytesCompleted() (n int64) {
	f.t.cl.rLock()
	n = f.bytesCompletedLocked()
	f.t.cl.rUnlock()
	return
}

func (f *File) bytesCompletedLocked() int64 {
	return f.length - f.bytesLeft()
}

func fileBytesLeft(
	torrentUsualPieceSize int64,
	fileFirstPieceIndex int,
	fileEndPieceIndex int,
	fileTorrentOffset int64,
	fileLength int64,
	torrentCompletedPieces *roaring.Bitmap,
	pieceSizeCompletedFn func(pieceIndex int) int64,
) (left int64) {
	if fileLength == 0 {
		return
	}

	noCompletedMiddlePieces := roaring.New()
	noCompletedMiddlePieces.AddRange(bitmap.BitRange(fileFirstPieceIndex), bitmap.BitRange(fileEndPieceIndex))
	noCompletedMiddlePieces.AndNot(torrentCompletedPieces)
	noCompletedMiddlePieces.Iterate(func(pieceIndex uint32) bool {
		i := int(pieceIndex)
		pieceSizeCompleted := pieceSizeCompletedFn(i)
		if i == fileFirstPieceIndex {
			beginOffset := fileTorrentOffset % torrentUsualPieceSize
			beginSize := torrentUsualPieceSize - beginOffset
			beginDownLoaded := pieceSizeCompleted - beginOffset
			if beginDownLoaded < 0 {
				beginDownLoaded = 0
			}
			left += beginSize - beginDownLoaded
		} else if i == fileEndPieceIndex-1 {
			endSize := (fileTorrentOffset + fileLength) % torrentUsualPieceSize
			if endSize == 0 {
				endSize = torrentUsualPieceSize
			}
			endDownloaded := pieceSizeCompleted
			if endDownloaded > endSize {
				endDownloaded = endSize
			}
			left += endSize - endDownloaded
		} else {
			left += torrentUsualPieceSize - pieceSizeCompleted
		}
		return true
	})

	if left > fileLength {
		left = fileLength
	}
	//
	//numPiecesSpanned := f.EndPieceIndex() - f.BeginPieceIndex()
	//completedMiddlePieces := f.t._completedPieces.Clone()
	//completedMiddlePieces.RemoveRange(0, bitmap.BitRange(f.BeginPieceIndex()+1))
	//completedMiddlePieces.RemoveRange(bitmap.BitRange(f.EndPieceIndex()-1), bitmap.ToEnd)
	//left += int64(numPiecesSpanned-2-pieceIndex(completedMiddlePieces.GetCardinality())) * torrentUsualPieceSize
	return
}

func (f *File) bytesLeft() (left int64) {
	return fileBytesLeft(
		int64(f.t.usualPieceSize()),
		f.BeginPieceIndex(),
		f.EndPieceIndex(),
		f.offset,
		f.length,
		&f.t._completedPieces,
		func(pieceIndex int) int64 {
			return int64(f.t.piece(pieceIndex).numDirtyBytes())
		},
	)
}

// The relative file path for a multi-file torrent, and the torrent name for a
// single-file torrent. Dir separators are '/'.
func (f *File) DisplayPath() string {
	return f.displayPath
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
	return f.t.newReader(f.Offset(), f.Length())
}

// Sets the minimum priority for pieces in the File.
func (f *File) SetPriority(prio PiecePriority) {
	f.t.cl.lock()
	if prio != f.prio {
		f.prio = prio
		f.t.updatePiecePriorities(f.BeginPieceIndex(), f.EndPieceIndex(), "File.SetPriority")
	}
	f.t.cl.unlock()
}

// Returns the priority per File.SetPriority.
func (f *File) Priority() (prio PiecePriority) {
	f.t.cl.rLock()
	prio = f.prio
	f.t.cl.rUnlock()
	return
}

// Returns the index of the first piece containing data for the file.
func (f *File) BeginPieceIndex() int {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex(f.offset / int64(f.t.usualPieceSize()))
}

// Returns the index of the piece after the last one containing data for the file.
func (f *File) EndPieceIndex() int {
	if f.t.usualPieceSize() == 0 {
		return 0
	}
	return pieceIndex((f.offset + f.length + int64(f.t.usualPieceSize()) - 1) / int64(f.t.usualPieceSize()))
}

func (f *File) numPieces() int {
	return f.EndPieceIndex() - f.BeginPieceIndex()
}
