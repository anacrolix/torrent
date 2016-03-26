package torrent

import "io"

// Represents data storage for a Torrent.
type Data interface {
	// Should return io.EOF only at end of torrent. Short reads due to missing
	// data should return io.ErrUnexpectedEOF.
	io.ReaderAt
	io.WriterAt
	// Bro, do you even io.Closer?
	Close()
	// Called when the client believes the piece data will pass a hash check.
	// The storage can move or mark the piece data as read-only as it sees
	// fit.
	PieceCompleted(index int) error
	// Returns true if the piece is complete.
	PieceComplete(index int) bool
}
