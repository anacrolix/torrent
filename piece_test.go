package torrent

import (
	"testing"
	"unsafe"
)

func TestPieceSize(t *testing.T) {
	t.Logf("%v", unsafe.Sizeof(Piece{}))
}
