package storage

import (
	"io"

	"github.com/anacrolix/torrent/metainfo"
)

type ClientImplCloser interface {
	ClientImpl
	Close() error
}

// Represents data storage for an unspecified torrent.
type ClientImpl interface {
	OpenTorrent(info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error)
}

// Data storage bound to a torrent.
type TorrentImpl struct {
	Piece func(p metainfo.Piece) PieceImpl
	Close func() error
	// Storages that share the same value, will provide a pointer to the same function.
	Capacity *func() *int64
}

// Interacts with torrent piece data. Optional interfaces to implement include io.WriterTo, such as
// when a piece supports a more efficient way to write out incomplete chunks
type PieceImpl interface {
	// These interfaces are not as strict as normally required. They can
	// assume that the parameters are appropriate for the dimensions of the
	// piece.
	io.ReaderAt
	io.WriterAt
	// Called when the client believes the piece data will pass a hash check.
	// The storage can move or mark the piece data as read-only as it sees
	// fit.
	MarkComplete() error
	MarkNotComplete() error
	// Returns true if the piece is complete.
	Completion() Completion
}

type Completion struct {
	Complete bool
	Ok       bool
}
