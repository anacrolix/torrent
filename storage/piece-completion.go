package storage

import (
	"os"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/metainfo"
)

type PieceCompletionGetSetter interface {
	Get(metainfo.PieceKey) (Completion, error)
	Set(_ metainfo.PieceKey, complete bool) error
}

// Implementations track the completion of pieces. It must be concurrent-safe.
type PieceCompletion interface {
	PieceCompletionGetSetter
	Close() error
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
