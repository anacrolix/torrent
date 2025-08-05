package requestStrategy

import (
	"github.com/tidwall/btree"
)

type tidwallBtree struct {
	tree     *btree.BTreeG[PieceRequestOrderItem]
	PathHint *btree.PathHint
}

func (me *tidwallBtree) Scan(f func(PieceRequestOrderItem) bool) {
	me.tree.Scan(f)
}

func NewTidwallBtree() *tidwallBtree {
	return &tidwallBtree{
		tree: btree.NewBTreeGOptions(
			func(a, b PieceRequestOrderItem) bool {
				return a.Less(&b)
			},
			btree.Options{NoLocks: true, Degree: 64}),
	}
}

func (me *tidwallBtree) Add(item PieceRequestOrderItem) {
	if _, ok := me.tree.SetHint(item, me.PathHint); ok {
		panic("shouldn't already have this")
	}
}

type PieceRequestOrderPathHint = btree.PathHint

func (me *tidwallBtree) Delete(item PieceRequestOrderItem) {
	_, deleted := me.tree.DeleteHint(item, me.PathHint)
	mustValue(deleted, item)
}

func (me *tidwallBtree) Contains(item PieceRequestOrderItem) bool {
	_, ok := me.tree.Get(item)
	return ok
}
