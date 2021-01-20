package torrent

import (
	"testing"
	"unsafe"
)

func TestStructSizes(t *testing.T) {
	t.Log("[]*File", unsafe.Sizeof([]*File(nil)))
	t.Log("Piece", unsafe.Sizeof(Piece{}))
	t.Log("map[*peer]struct{}", unsafe.Sizeof(map[*Peer]struct{}(nil)))
}
