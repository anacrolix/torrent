package peer_protocol

import (
	"testing"
)

func TestConstants(t *testing.T) {
	// check that iota works as expected in the const block
	if NotInterested != 3 {
		t.FailNow()
	}
}
