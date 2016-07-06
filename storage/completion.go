package storage

import (
	"log"

	"github.com/anacrolix/torrent/metainfo"
)

type pieceCompletion interface {
	Get(metainfo.Piece) bool
	Set(metainfo.Piece, bool)
	Close()
}

func pieceCompletionForDir(dir string) (ret pieceCompletion) {
	ret, err := newDBPieceCompletion(dir)
	if err != nil {
		log.Printf("couldn't open piece completion db in %q: %s", dir, err)
		ret = new(mapPieceCompletion)
	}
	return
}
