package storage

import (
	"github.com/anacrolix/torrent/metainfo"
)

type mapPieceCompletion struct {
	m map[metainfo.PieceKey]struct{}
}

func (mapPieceCompletion) Close() {}

func (me *mapPieceCompletion) Get(p metainfo.Piece) bool {
	_, ok := me.m[p.Key()]
	return ok
}

func (me *mapPieceCompletion) Set(p metainfo.Piece, b bool) {
	if b {
		if me.m == nil {
			me.m = make(map[metainfo.PieceKey]struct{})
		}
		me.m[p.Key()] = struct{}{}
	} else {
		delete(me.m, p.Key())
	}
}
