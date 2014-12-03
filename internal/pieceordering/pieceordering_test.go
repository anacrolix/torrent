package pieceordering

import (
	"testing"
)

func checkOrder(t *testing.T, i *Instance, pp []int) {
	e := i.First()
	for _, p := range pp {
		if p != e.Piece() {
			t.FailNow()
		}
		e = e.Next()
	}
	if e != nil {
		t.FailNow()
	}
}

func TestPieceOrdering(t *testing.T) {
	i := New()
	i.SetPiece(0, 1)
	i.SetPiece(1, 0)
	checkOrder(t, i, []int{1, 0})
	i.SetPiece(1, 2)
	checkOrder(t, i, []int{0, 1})
	i.RemovePiece(1)
	checkOrder(t, i, []int{0})
	i.RemovePiece(2)
	i.RemovePiece(1)
	checkOrder(t, i, []int{0})
	i.RemovePiece(0)
}
