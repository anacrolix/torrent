package storage

import (
	"cmp"
	"iter"
	"os"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types/infohash"
)

type PieceCompletionGetSetter interface {
	// I think the extra error parameter is vestigial. Looks like you should put your error in
	// Completion.Err.
	Get(metainfo.PieceKey) (Completion, error)
	Set(_ metainfo.PieceKey, complete bool) error
}

// Implementations track the completion of pieces. It must be concurrent-safe.
type PieceCompletion interface {
	PieceCompletionGetSetter
	// Piece completion is maintained between storage instances. We can use this to avoid flushing
	// pieces when they're marked complete if there's some other mechanism to ensure correctness.
	Close() error
}

type PieceCompletionPersistenter interface {
	Persistent() bool
}

func pieceCompletionIsPersistent(pc PieceCompletion) bool {
	if p, ok := pc.(PieceCompletionPersistenter); ok {
		return p.Persistent()
	}
	// Default is true. That's assumes flushing is required every time a piece is completed.
	return true
}

// Optional interface with optimized Get for ranges. Use GetPieceCompletionRange wrapper to abstract
// over it not being implemented.
type PieceCompletionGetRanger interface {
	GetRange(_ infohash.T, begin, end int) iter.Seq[Completion]
}

// Get piece completion as an iterator. Should be faster for long sequences of Gets. Uses optional
// interface PieceCompletionGetRanger if implemented.
func GetPieceCompletionRange(pc PieceCompletion, ih infohash.T, begin, end int) iter.Seq[Completion] {
	if a, ok := pc.(PieceCompletionGetRanger); ok {
		return a.GetRange(ih, begin, end)
	}
	return func(yield func(Completion) bool) {
		for i := begin; i < end; i++ {
			c, err := pc.Get(metainfo.PieceKey{
				InfoHash: ih,
				Index:    i,
			})
			c.Err = cmp.Or(c.Err, err)
			if !yield(c) {
				return
			}
		}
	}
}

func pieceCompletionForDir(dir string) (ret PieceCompletion) {
	// This should be happening before sqlite attempts to open a database in the intended directory.
	os.MkdirAll(dir, 0o700)
	ret, err := NewDefaultPieceCompletionForDir(dir)
	if err != nil {
		// This kinda sux using the global logger. This code is ancient.
		log.Levelf(log.Warning, "couldn't open piece completion db in %q: %s", dir, err)
		ret = NewMapPieceCompletion()
	}
	return
}
