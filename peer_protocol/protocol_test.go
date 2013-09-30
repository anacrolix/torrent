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
func TestBitfieldEncode(t *testing.T) {
	bm := make(Bitfield, 37)
	bm[2] = true
	bm[7] = true
	bm[32] = true
	s := string(bm.Encode())
	const expected = "\x21\x00\x00\x00\x80"
	if s != expected {
		t.Fatalf("got %#v, expected %#v", s, expected)
	}
}

func TestHaveEncode(t *testing.T) {
	actual := string(Have(42).Encode())
	expected := "\x04\x00\x00\x00\x2a"
	if actual != expected {
		t.Fatalf("expected %#v, got %#v", expected, actual)
	}
}
