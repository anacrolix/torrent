package storage

import (
	"errors"
	"io"

	"github.com/james-lawrence/torrent/metainfo"
)

// Represents data storage for an unspecified torrent.
type ClientImpl interface {
	OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error)
	Close() error
}

// Data storage bound to a torrent.
type TorrentImpl interface {
	io.ReaderAt
	io.WriterAt
	Close() error
}

type Completion struct {
	Complete bool
	Ok       bool
}

func ErrClosed() error {
	return errors.New("storage closed")
}
