package storage

import (
	"io"
	"log"
	"os"

	"github.com/anacrolix/torrent/metainfo"
)

type filePieceImpl struct {
	*fileTorrentImpl
	p metainfo.Piece
	io.WriterAt
	io.ReaderAt
}

var _ PieceImpl = (*filePieceImpl)(nil)

func (me *filePieceImpl) pieceKey() metainfo.PieceKey {
	return metainfo.PieceKey{me.infoHash, me.p.Index()}
}

func (fs *filePieceImpl) Completion() Completion {
	c, err := fs.completion.Get(fs.pieceKey())
	if err != nil {
		log.Printf("error getting piece completion: %s", err)
		c.Ok = false
		return c
	}

	verified := true
	if c.Complete {
		// If it's allegedly complete, check that its constituent files have the necessary length.
		for _, fi := range extentCompleteRequiredLengths(fs.p.Info, fs.p.Offset(), fs.p.Length()) {
			s, err := os.Stat(fs.files[fi.fileIndex].path)
			if err != nil || s.Size() < fi.length {
				verified = false
				break
			}
		}
	}

	if !verified {
		// The completion was wrong, fix it.
		c.Complete = false
		fs.completion.Set(fs.pieceKey(), false, false)
	}

	return c
}

func (fs *filePieceImpl) MarkComplete(awaitFlush bool) error {
	return fs.completion.Set(fs.pieceKey(), true, awaitFlush)
}

func (fs *filePieceImpl) MarkNotComplete() error {
	return fs.completion.Set(fs.pieceKey(), false, false)
}

func (fs *filePieceImpl) IsNew() bool {
	return fs.created
}
