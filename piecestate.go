package torrent

import (
	"github.com/anacrolix/torrent/storage"
)

// The current state of a piece.
type PieceState struct {
	Priority PiecePriority
	storage.Completion
	// The piece is being hashed, or is queued for hash. Deprecated: Use those fields instead.
	Checking bool

	Hashing       bool
	QueuedForHash bool
	// The piece state is being marked in the storage.
	Marking bool

	// Some of the piece has been obtained.
	Partial bool

	// The v2 hash for the piece layer is missing.
	MissingPieceLayerHash bool
}

// Represents a series of consecutive pieces with the same state.
type PieceStateRun struct {
	PieceState
	Length int // How many consecutive pieces have this state.
}
