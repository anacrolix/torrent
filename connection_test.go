package torrent

import (
	"testing"
	"time"

	"github.com/bradfitz/iter"

	"bitbucket.org/anacrolix/go.torrent/internal/pieceordering"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

func TestCancelRequestOptimized(t *testing.T) {
	c := &connection{
		PeerMaxRequests: 1,
		PeerPieces:      []bool{false, true},
		post:            make(chan peer_protocol.Message),
		writeCh:         make(chan []byte),
	}
	if len(c.Requests) != 0 {
		t.FailNow()
	}
	// Keepalive timeout of 0 works because I'm just that good.
	go c.writeOptimizer(0 * time.Millisecond)
	c.Request(newRequest(1, 2, 3))
	if len(c.Requests) != 1 {
		t.Fatal("request was not posted")
	}
	// Posting this message should removing the pending Request.
	if !c.Cancel(newRequest(1, 2, 3)) {
		t.Fatal("request was not found")
	}
	// Check that the write optimization has filtered out the Request message.
	for _, b := range []string{
		// The initial request triggers an Interested message.
		"\x00\x00\x00\x01\x02",
		// Let a keep-alive through to verify there were no pending messages.
		"\x00\x00\x00\x00",
	} {
		bb := string(<-c.writeCh)
		if b != bb {
			t.Fatalf("received message %q is not expected: %q", bb, b)
		}
	}
	close(c.post)
	// Drain the write channel until it closes.
	for b := range c.writeCh {
		bs := string(b)
		if bs != "\x00\x00\x00\x00" {
			t.Fatal("got unexpected non-keepalive")
		}
	}
}

func testRequestOrder(expected []int, ro *pieceordering.Instance, t *testing.T) {
	e := ro.First()
	for _, i := range expected {
		if i != e.Piece() {
			t.FailNow()
		}
		e = e.Next()
	}
	if e != nil {
		t.FailNow()
	}
}

// Tests the request ordering based on a connections priorities.
func TestPieceRequestOrder(t *testing.T) {
	c := connection{
		pieceRequestOrder: pieceordering.New(),
		piecePriorities:   []int{1, 4, 0, 3, 2},
	}
	testRequestOrder(nil, c.pieceRequestOrder, t)
	c.pendPiece(2, piecePriorityNone)
	testRequestOrder(nil, c.pieceRequestOrder, t)
	c.pendPiece(1, piecePriorityNormal)
	c.pendPiece(2, piecePriorityNormal)
	testRequestOrder([]int{2, 1}, c.pieceRequestOrder, t)
	c.pendPiece(0, piecePriorityNormal)
	testRequestOrder([]int{2, 0, 1}, c.pieceRequestOrder, t)
	c.pendPiece(1, piecePriorityReadahead)
	testRequestOrder([]int{1, 2, 0}, c.pieceRequestOrder, t)
	c.pendPiece(4, piecePriorityNow)
	testRequestOrder([]int{4, 1, 2, 0}, c.pieceRequestOrder, t)
	c.pendPiece(2, piecePriorityReadahead)
	// N(4), R(1, 2), N(0)
	testRequestOrder([]int{4, 1, 2, 0}, c.pieceRequestOrder, t)
	// Note this intentially sets to None a piece that's not in the order.
	for i := range iter.N(5) {
		c.pendPiece(i, piecePriorityNone)
	}
	testRequestOrder(nil, c.pieceRequestOrder, t)
}
