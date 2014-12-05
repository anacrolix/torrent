package pieceordering

import (
	"testing"
)

func instanceSlice(i *Instance) (sl []int) {
	for e := i.First(); e != nil; e = e.Next() {
		sl = append(sl, e.Piece())
	}
	return
}

func checkOrder(t *testing.T, i *Instance, pp []int) {
	fatal := func() {
		t.Fatalf("have %v, expected %v", instanceSlice(i), pp)
	}
	e := i.First()
	for _, p := range pp {
		if p != e.Piece() {
			fatal()
		}
		e = e.Next()
	}
	if e != nil {
		fatal()
	}
}

func TestPieceOrdering(t *testing.T) {
	i := New()
	i.SetPiece(0, 1)
	i.SetPiece(1, 0)
	checkOrder(t, i, []int{1, 0})
	i.SetPiece(1, 2)
	checkOrder(t, i, []int{0, 1})
	i.DeletePiece(1)
	checkOrder(t, i, []int{0})
	i.DeletePiece(2)
	i.DeletePiece(1)
	checkOrder(t, i, []int{0})
	i.DeletePiece(0)
	checkOrder(t, i, nil)
	i.SetPiece(2, 1)
	i.SetPiece(1, 1)
	i.SetPiece(3, 1)
	checkOrder(t, i, []int{3, 1, 2})
	// Move a piece that isn't the youngest in a key.
	i.SetPiece(1, -1)
	checkOrder(t, i, []int{1, 3, 2})
	i.DeletePiece(2)
	i.DeletePiece(3)
	i.DeletePiece(1)
	checkOrder(t, i, nil)
	i.DeletePiece(2)
	i.DeletePiece(3)
	i.DeletePiece(1)
	checkOrder(t, i, nil)
}
