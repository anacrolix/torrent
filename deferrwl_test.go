package torrent

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestUniqueDeferOnce(t *testing.T) {
	var p1, p2 Piece
	var mu lockWithDeferreds
	mu.Lock()
	mu.DeferUniqueUnaryFunc(&p1, p1.publishStateChange)
	mu.DeferUniqueUnaryFunc(&p1, p1.publishStateChange)
	qt.Assert(t, qt.HasLen(mu.unlockActions, 1))
	mu.DeferUniqueUnaryFunc(&p2, p2.publishStateChange)
	qt.Assert(t, qt.HasLen(mu.unlockActions, 2))
}
