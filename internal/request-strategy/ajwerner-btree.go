package requestStrategy

import (
	"github.com/anacrolix/btree"
)

type ajwernerBtree struct {
	btree btree.Set[PieceRequestOrderItem]
}

func (a *ajwernerBtree) Contains(item PieceRequestOrderItem) bool {
	_, ok := a.btree.Get(item)
	return ok
}

var _ Btree = (*ajwernerBtree)(nil)

func NewAjwernerBtree() *ajwernerBtree {
	return &ajwernerBtree{
		btree: btree.MakeSet(func(t, t2 PieceRequestOrderItem) int {
			return pieceOrderLess(&t, &t2).OrderingInt()
		}),
	}
}

func mustValue[V any](b bool, panicValue V) {
	if !b {
		panic(panicValue)
	}
}

func (a *ajwernerBtree) Delete(item PieceRequestOrderItem) {
	mustValue(a.btree.Delete(item), item)
}

func (a *ajwernerBtree) Add(item PieceRequestOrderItem) {
	_, overwrote := a.btree.Upsert(item)
	mustValue(!overwrote, item)
}

func (a *ajwernerBtree) Scan(f func(PieceRequestOrderItem) bool) {
	it := a.btree.Iterator()
	for it.First(); it.Valid(); it.Next() {
		if !f(it.Cur()) {
			break
		}
	}
}
