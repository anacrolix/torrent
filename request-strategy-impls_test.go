package torrent

import (
	"io"
	"runtime"
	"testing"

	"github.com/davecgh/go-spew/spew"
	qt "github.com/frankban/quicktest"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

func makeRequestStrategyPiece(t request_strategy.Torrent) request_strategy.Piece {
	return t.Piece(0)
}

func TestRequestStrategyPieceDoesntAlloc(t *testing.T) {
	c := qt.New(t)
	akshalTorrent := &Torrent{pieces: make([]Piece, 1)}
	rst := requestStrategyTorrent{akshalTorrent}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p := makeRequestStrategyPiece(rst)
	runtime.ReadMemStats(&after)
	c.Assert(before.HeapAlloc, qt.Equals, after.HeapAlloc)
	// We have to use p, or it gets optimized away.
	spew.Fdump(io.Discard, p)
}
