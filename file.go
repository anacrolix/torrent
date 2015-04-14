package torrent

import "github.com/anacrolix/libtorgo/metainfo"

// Provides access to regions of torrent data that correspond to its files.
type File struct {
	t      Torrent
	path   string
	offset int64
	length int64
	fi     metainfo.FileInfo
}

// Data for this file begins this far into the torrent.
func (f *File) Offset() int64 {
	return f.offset
}

func (f File) FileInfo() metainfo.FileInfo {
	return f.fi
}

func (f File) Path() string {
	return f.path
}

func (f *File) Length() int64 {
	return f.length
}

type FilePieceState struct {
	Length int64
	State  byte
}

func (f *File) Progress() (ret []FilePieceState) {
	pieceSize := int64(f.t.usualPieceSize())
	off := f.offset % pieceSize
	remaining := f.length
	for i := int(f.offset / pieceSize); ; i++ {
		if remaining == 0 {
			break
		}
		len1 := pieceSize - off
		if len1 > remaining {
			len1 = remaining
		}
		ret = append(ret, FilePieceState{len1, f.t.pieceStatusChar(i)})
		off = 0
		remaining -= len1
	}
	return
}

func (f *File) PrioritizeRegion(off, len int64) {
	if off < 0 || off >= f.length {
		return
	}
	if off+len > f.length {
		len = f.length - off
	}
	off += f.offset
	f.t.SetRegionPriority(off, len)
}
