package requestStrategy

import "github.com/anacrolix/torrent/metainfo"

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
	InfoHash metainfo.Hash
	Index    int
}

type PieceRequestOrderState struct {
	Priority     piecePriority
	Partial      bool
	Availability int
}

type pieceRequestOrderItem struct {
	key   PieceRequestOrderKey
	state PieceRequestOrderState
}

func (me *pieceRequestOrderItem) Less(otherConcrete *pieceRequestOrderItem) bool {
	return pieceOrderLess(me, otherConcrete).Less()
}

func (me *PieceRequestOrder) Add(key PieceRequestOrderKey, state PieceRequestOrderState) {
	if _, ok := me.keys[key]; ok {
		panic(key)
	}
	me.tree.Add(pieceRequestOrderItem{key, state})
	me.keys[key] = state
}

func (me *PieceRequestOrder) Update(
	key PieceRequestOrderKey,
	state PieceRequestOrderState,
) {
	oldState, ok := me.keys[key]
	if !ok {
		panic("key should have been added already")
	}
	if state == oldState {
		return
	}
	me.tree.Delete(pieceRequestOrderItem{key, oldState})
	me.tree.Add(pieceRequestOrderItem{key, state})
	me.keys[key] = state
}

func (me *PieceRequestOrder) existingItemForKey(key PieceRequestOrderKey) pieceRequestOrderItem {
	return pieceRequestOrderItem{
		key:   key,
		state: me.keys[key],
	}
}

func (me *PieceRequestOrder) Delete(key PieceRequestOrderKey) {
	me.tree.Delete(pieceRequestOrderItem{key, me.keys[key]})
	delete(me.keys, key)
}

func (me *PieceRequestOrder) Len() int {
	return len(me.keys)
}
