package storage

import (
	"io"
	"log"
	"os"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/segments"
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
		if !fs.segmentLocater.Locate(segments.Extent{
			Start:  fs.p.Offset(),
			Length: fs.p.Length(),
		}, func(i int, extent segments.Extent) bool {
			file := fs.files[i]
			s, err := os.Stat(file.path)
			if err != nil || s.Size() < extent.Start+extent.Length {
				verified = false
				return false
			}
			return true
		}) {
			panic("files do not cover piece extent")
		}
	}

	if !verified {
		// The completion was wrong, fix it.
		c.Complete = false
		fs.completion.Set(fs.pieceKey(), false)
	}

	return c
}

func (fs *filePieceImpl) MarkComplete() error {
	return fs.completion.Set(fs.pieceKey(), true)
}

func (fs *filePieceImpl) MarkNotComplete() error {
	return fs.completion.Set(fs.pieceKey(), false)
}
