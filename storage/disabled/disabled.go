package disabled

import (
	"errors"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type Client struct{}

var capacity int64

func (c Client) OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	capFunc := func() *int64 {
		return &capacity
	}
	return storage.TorrentImpl{
		Piece: func(piece metainfo.Piece) storage.PieceImpl {
			return Piece{}
		},
		Close: func() error {
			return nil
		},
		Capacity: &capFunc,
	}, nil
}

func (c Client) capacity() *int64 {
	return &capacity
}

type Piece struct{}

func (Piece) ReadAt(p []byte, off int64) (n int, err error) {
	err = errors.New("disabled")
	return
}

func (Piece) WriteAt(p []byte, off int64) (n int, err error) {
	err = errors.New("disabled")
	return
}

func (Piece) MarkComplete() error {
	return errors.New("disabled")
}

func (Piece) MarkNotComplete() error {
	return errors.New("disabled")
}

func (Piece) Completion() storage.Completion {
	return storage.Completion{
		Complete: false,
		Ok:       true,
	}
}
