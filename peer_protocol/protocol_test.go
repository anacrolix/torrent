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
	bf := make([]bool, 37)
	bf[2] = true
	bf[7] = true
	bf[32] = true
	s := string(marshalBitfield(bf))
	const expected = "\x21\x00\x00\x00\x80"
	if s != expected {
		t.Fatalf("got %#v, expected %#v", s, expected)
	}
}

func TestBitfieldUnmarshal(t *testing.T) {
	bf := unmarshalBitfield([]byte("\x81\x06"))
	expected := make([]bool, 16)
	expected[0] = true
	expected[7] = true
	expected[13] = true
	expected[14] = true
	if len(bf) != len(expected) {
		t.FailNow()
	}
	for i := range expected {
		if bf[i] != expected[i] {
			t.FailNow()
		}
	}
}

func TestHaveEncode(t *testing.T) {
	actualBytes, err := Message{
		Type:  Have,
		Index: 42,
	}.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	actualString := string(actualBytes)
	expected := "\x00\x00\x00\x05\x04\x00\x00\x00\x2a"
	if actualString != expected {
		t.Fatalf("expected %#v, got %#v", expected, actualString)
	}
}
