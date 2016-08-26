package storage

import (
	"io"
	"os"

	"github.com/anacrolix/torrent/metainfo"
)

type fileStoragePiece struct {
	*fileTorrentStorage
	p metainfo.Piece
	io.WriterAt
	r io.ReaderAt
}

func (me *fileStoragePiece) pieceKey() metainfo.PieceKey {
	return metainfo.PieceKey{me.infoHash, me.p.Index()}
}

func (fs *fileStoragePiece) GetIsComplete() bool {
	ret, err := fs.completion.Get(fs.pieceKey())
	if err != nil || !ret {
		return false
	}
	// If it's allegedly complete, check that its constituent files have the
	// necessary length.
	for _, fi := range extentCompleteRequiredLengths(fs.p.Info, fs.p.Offset(), fs.p.Length()) {
		s, err := os.Stat(fs.fileInfoName(fi))
		if err != nil || s.Size() < fi.Length {
			ret = false
			break
		}
	}
	if ret {
		return true
	}
	// The completion was wrong, fix it.
	fs.completion.Set(fs.pieceKey(), false)
	return false
}

func (fs *fileStoragePiece) MarkComplete() error {
	fs.completion.Set(fs.pieceKey(), true)
	return nil
}

func (fsp *fileStoragePiece) ReadAt(b []byte, off int64) (n int, err error) {
	n, err = fsp.r.ReadAt(b, off)
	if n != 0 {
		err = nil
		return
	}
	if off < 0 || off >= fsp.p.Length() {
		return
	}
	fsp.completion.Set(fsp.pieceKey(), false)
	return
}
