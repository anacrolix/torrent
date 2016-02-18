package torrent

import "io"

// Represents data storage for a Torrent.
type Data interface {
	io.ReaderAt
	io.WriterAt
	// Bro, do you even io.Closer?
	Close()
	// We believe the piece data will pass a hash check.
	PieceCompleted(index int) error
	// Returns true if the piece is complete.
	PieceComplete(index int) bool
}
