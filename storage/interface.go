package storage

import (
	"io"

	"github.com/anacrolix/torrent/metainfo"
)

// Represents data storage for a Torrent.
type I interface {
	Piece(metainfo.Piece) Piece
}

type Piece interface {
	// Should return io.EOF only at end of torrent. Short reads due to missing
	// data should return io.ErrUnexpectedEOF.
	io.ReaderAt
	io.WriterAt
	// Called when the client believes the piece data will pass a hash check.
	// The storage can move or mark the piece data as read-only as it sees
	// fit.
	MarkComplete() error
	// Returns true if the piece is complete.
	GetIsComplete() bool
}

// type PieceStorage interface {
// 	ReadAt(metainfo.Piece, []byte, int64) (int, error)
// 	WriteAt(metainfo.Piece, []byte, int64) (int, error)
// 	MarkComplete(metainfo.Piece) error
// 	GetIsComplete(metainfo.Piece) bool
// }

// type wrappedPieceStorage struct {
// 	ps PieceStorage
// }

// func WrapPieceStorage(ps PieceStorage) I {
// 	return wrappedPieceStorage{ps}
// }

// func (me wrappedPieceStorage) Piece(metainfo.Piece)
