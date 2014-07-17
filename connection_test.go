package torrent

import (
	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
	"testing"
	"time"
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
