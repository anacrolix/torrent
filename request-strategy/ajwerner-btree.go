package requestStrategy

import (
	"github.com/ajwerner/btree"
)

type ajwernerBtree struct {
	btree btree.Set[pieceRequestOrderItem]
}

var _ Btree = (*ajwernerBtree)(nil)

func NewAjwernerBtree() *ajwernerBtree {
	return &ajwernerBtree{
		btree: btree.MakeSet(func(t, t2 pieceRequestOrderItem) int {
			return pieceOrderLess(&t, &t2).OrderingInt()
		}),
	}
}

func mustValue[V any](b bool, panicValue V) {
	if !b {
		panic(panicValue)
	}
}

func (a *ajwernerBtree) Delete(item pieceRequestOrderItem) {
	mustValue(a.btree.Delete(item), item)
}

func (a *ajwernerBtree) Add(item pieceRequestOrderItem) {
	_, overwrote := a.btree.Upsert(item)
	mustValue(!overwrote, item)
}

func (a *ajwernerBtree) Scan(f func(pieceRequestOrderItem) bool) {
	it := a.btree.Iterator()
	it.First()
	for it.First(); it.Valid(); it.Next() {
		if !f(it.Cur()) {
			break
		}
	}
}
