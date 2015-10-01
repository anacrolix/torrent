package torrent

import "io"

// Represents data storage for a Torrent.
type Data interface {
	ReadAt(p []byte, off int64) (n int, err error)
	Close()
	WriteAt(p []byte, off int64) (n int, err error)
	WriteSectionTo(w io.Writer, off, n int64) (written int64, err error)
	// We believe the piece data will pass a hash check.
	PieceCompleted(index int) error
	// Returns true if the piece is complete.
	PieceComplete(index int) bool
}
