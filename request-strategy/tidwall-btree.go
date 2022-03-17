package request_strategy

import (
	"github.com/tidwall/btree"
)

type tidwallBtree struct {
	tree     *btree.BTree[pieceRequestOrderItem]
	PathHint *btree.PathHint
}

func (me *tidwallBtree) Scan(f func(pieceRequestOrderItem) bool) {
	me.tree.Scan(f)
}

func NewTidwallBtree() *tidwallBtree {
	return &tidwallBtree{
		tree: btree.NewOptions(
			func(a, b pieceRequestOrderItem) bool {
				return a.Less(&b)
			},
			btree.Options{NoLocks: true}),
	}
}

func (me *tidwallBtree) Add(item pieceRequestOrderItem) {
	if _, ok := me.tree.SetHint(item, me.PathHint); ok {
		panic("shouldn't already have this")
	}
}

type PieceRequestOrderPathHint = btree.PathHint

func (me *tidwallBtree) Delete(item pieceRequestOrderItem) {
	_, deleted := me.tree.DeleteHint(item, me.PathHint)
	mustValue(deleted, item)
}
