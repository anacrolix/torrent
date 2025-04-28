package storage

import (
	"io"
)

type TorrentReader interface {
	io.ReaderAt
	io.Closer
}
