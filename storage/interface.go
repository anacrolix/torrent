package storage

import (
	"context"
	"io"

	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
)

type ClientImplCloser interface {
	ClientImpl
	Close() error
}

// Represents data storage for an unspecified torrent.
type ClientImpl interface {
	OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (TorrentImpl, error)
}

// Returning a negative cap, can we indicate there's no specific cap? If this is not-nil we use it
// as a key into piece request order. The capped bool also needs to be true to be truly capped
// though.
type TorrentCapacity *func() (cap int64, capped bool)

// Data storage bound to a torrent.
type TorrentImpl struct {
	// v2 infos might not have the piece hash available even if we have the info. The
	// metainfo.Piece.Hash method was removed to enforce this.
	Piece func(p metainfo.Piece) PieceImpl
	// Preferred over PieceWithHash. Called with the piece hash if it's available.
	PieceWithHash func(p metainfo.Piece, pieceHash g.Option[[]byte]) PieceImpl
	Close         func() error
	// Storages that share the same space, will provide equal pointers. The function is called once
	// to determine the storage for torrents sharing the same function pointer, and mutated in
	// place.
	Capacity TorrentCapacity

	NewReader      func() TorrentReader
	NewPieceReader func(p Piece) PieceReader
}

// Interacts with torrent piece data. Optional interfaces to implement include://
//
//		io.WriterTo, such as when a piece supports a more efficient way to write out incomplete chunks.
//		SelfHashing, such as when a piece supports a more efficient way to hash its contents.
//	 	PieceReaderer when it has a stateful Reader interface that is more efficient.
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
	// Returns the state of a piece. Typically, this is implemented in some kind of storage to avoid
	// rehashing, and cheap checks are performed here. (The implementation maintains a cache in
	// Torrent).
	Completion() Completion
}

// Completion state of a piece.
type Completion struct {
	Err error
	// The state is known or cached.
	Ok bool
	// If Ok, whether the data is correct. TODO: Check all callsites test Ok first.
	Complete bool
}

// Allows a storage backend to override hashing (i.e. if it can do it more efficiently than the
// torrent client can).
type SelfHashing interface {
	SelfHash() (metainfo.Hash, error)
}

// Piece supports dedicated reader.
type PieceReaderer interface {
	NewReader() (PieceReader, error)
}

type PieceReader interface {
	io.ReaderAt
	io.Closer
}
