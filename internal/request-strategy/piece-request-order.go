package requestStrategy

import (
	"iter"
	"unique"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/metainfo"
)

type Btree interface {
	Delete(PieceRequestOrderItem)
	Add(PieceRequestOrderItem)
	// TODO: Add an iterator variant of this and benchmark.
	Scan(func(PieceRequestOrderItem) bool)
	Contains(PieceRequestOrderItem) bool
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
	// It should be the canonical short hash.
	InfoHash unique.Handle[metainfo.Hash]
	Index    int
}

type PieceRequestOrderState struct {
	Availability int
	Priority     piecePriority
	Partial      bool
}

type PieceRequestOrderItem struct {
	Key   PieceRequestOrderKey
	State PieceRequestOrderState
}

func (me *PieceRequestOrderItem) Less(otherConcrete *PieceRequestOrderItem) bool {
	return pieceOrderLess(me, otherConcrete).Less()
}

// Returns the old state if the key was already present. The Update method needs to look at it.
func (me *PieceRequestOrder) Add(
	key PieceRequestOrderKey,
	state PieceRequestOrderState,
) (old g.Option[PieceRequestOrderState]) {
	if old.Value, old.Ok = me.keys[key]; old.Ok {
		if state == old.Value {
			return
		}
		me.tree.Delete(PieceRequestOrderItem{key, old.Value})
	}
	me.tree.Add(PieceRequestOrderItem{key, state})
	me.keys[key] = state
	return
}

func (me *PieceRequestOrder) Update(
	key PieceRequestOrderKey,
	state PieceRequestOrderState,
) (changed bool) {
	old := me.Add(key, state)
	if !old.Ok {
		panic("Key should have been added already")
	}
	return old.Value != state
}

func (me *PieceRequestOrder) Delete(key PieceRequestOrderKey) (deleted bool) {
	state, ok := me.keys[key]
	if !ok {
		return false
	}
	me.tree.Delete(PieceRequestOrderItem{key, state})
	delete(me.keys, key)
	return true
}

func (me *PieceRequestOrder) Len() int {
	return len(me.keys)
}

func (me *PieceRequestOrder) Iter() iter.Seq[PieceRequestOrderItem] {
	return func(yield func(PieceRequestOrderItem) bool) {
		me.tree.Scan(func(item PieceRequestOrderItem) bool {
			return yield(item)
		})
	}
}

func (me *PieceRequestOrder) Get(key PieceRequestOrderKey) (ret g.Option[PieceRequestOrderState]) {
	ret.Value, ret.Ok = me.keys[key]
	panicif.NotEq(ret.Ok, me.tree.Contains(PieceRequestOrderItem{key, ret.Value}))
	return
}
