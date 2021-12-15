package request_strategy

import (
	"fmt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/tidwall/btree"
)

func NewPieceOrder() *PieceRequestOrder {
	return &PieceRequestOrder{
		tree: btree.NewOptions(
			func(a, b pieceRequestOrderItem) bool {
				return a.Less(&b)
			},
			btree.Options{NoLocks: true}),
		keys: make(map[PieceRequestOrderKey]PieceRequestOrderState),
	}
}

type PieceRequestOrder struct {
	tree     *btree.BTree[pieceRequestOrderItem]
	keys     map[PieceRequestOrderKey]PieceRequestOrderState
	pathHint btree.PathHint
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
	if _, ok := me.tree.SetHint(pieceRequestOrderItem{
		key:   key,
		state: state,
	}, &me.pathHint); ok {
		panic("shouldn't already have this")
	}
	me.keys[key] = state
}

type PieceRequestOrderPathHint = btree.PathHint

func (me *PieceRequestOrder) Update(key PieceRequestOrderKey, state PieceRequestOrderState) {
	oldState, ok := me.keys[key]
	if !ok {
		panic("key should have been added already")
	}
	if state == oldState {
		return
	}
	item := pieceRequestOrderItem{
		key:   key,
		state: oldState,
	}
	if _, ok := me.tree.DeleteHint(item, &me.pathHint); !ok {
		panic(fmt.Sprintf("%#v", key))
	}
	item.state = state
	if _, ok := me.tree.SetHint(item, &me.pathHint); ok {
		panic(key)
	}
	me.keys[key] = state
}

func (me *PieceRequestOrder) existingItemForKey(key PieceRequestOrderKey) pieceRequestOrderItem {
	return pieceRequestOrderItem{
		key:   key,
		state: me.keys[key],
	}
}

func (me *PieceRequestOrder) Delete(key PieceRequestOrderKey) {
	item := me.existingItemForKey(key)
	if _, ok := me.tree.DeleteHint(item, &me.pathHint); !ok {
		panic(key)
	}
	delete(me.keys, key)
}

func (me *PieceRequestOrder) Len() int {
	return len(me.keys)
}
