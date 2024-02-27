package requestStrategy

import (
	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
)

type Btree interface {
	Delete(pieceRequestOrderItem)
	Add(pieceRequestOrderItem)
	Scan(func(pieceRequestOrderItem) bool)
}

func NewPieceOrder(btree Btree, cap int) *PieceRequestOrder {
	return &PieceRequestOrder{
		tree: btree,
		keys: make(map[PieceRequestOrderKey]PieceRequestOrderState, cap),
	}
}

type PieceRequestOrder struct {
	tree Btree
	keys map[PieceRequestOrderKey]PieceRequestOrderState
}

type PieceRequestOrderKey struct {
	Index    int
	InfoHash metainfo.Hash
}

type PieceRequestOrderState struct {
	Availability int
	Priority     piecePriority
	Partial      bool
}

type pieceRequestOrderItem struct {
	key   PieceRequestOrderKey
	state PieceRequestOrderState
}

func (me *pieceRequestOrderItem) Less(otherConcrete *pieceRequestOrderItem) bool {
	return pieceOrderLess(me, otherConcrete).Less()
}

// Returns the old state if the key was already present.
func (me *PieceRequestOrder) Add(
	key PieceRequestOrderKey,
	state PieceRequestOrderState,
) (old g.Option[PieceRequestOrderState]) {
	if old.Value, old.Ok = me.keys[key]; old.Ok {
		if state == old.Value {
			return
		}
		me.tree.Delete(pieceRequestOrderItem{key, old.Value})
	}
	me.tree.Add(pieceRequestOrderItem{key, state})
	me.keys[key] = state
	return
}

func (me *PieceRequestOrder) Update(
	key PieceRequestOrderKey,
	state PieceRequestOrderState,
) {
	if !me.Add(key, state).Ok {
		panic("key should have been added already")
	}
}

func (me *PieceRequestOrder) existingItemForKey(key PieceRequestOrderKey) pieceRequestOrderItem {
	return pieceRequestOrderItem{
		key:   key,
		state: me.keys[key],
	}
}

func (me *PieceRequestOrder) Delete(key PieceRequestOrderKey) bool {
	state, ok := me.keys[key]
	if !ok {
		return false
	}
	me.tree.Delete(pieceRequestOrderItem{key, state})
	delete(me.keys, key)
	return true
}

func (me *PieceRequestOrder) Len() int {
	return len(me.keys)
}
