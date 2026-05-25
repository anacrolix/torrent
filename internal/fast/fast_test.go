package fast

import (
	"net"
	"reflect"
	"testing"
)

// TestGenerateFastSet_BEP6Example verifies our output against the canonical
// example from BEP 6:
//
//	ip        = 80.4.4.200  (masked to 80.4.4.0)
//	infohash  = 0xAAAAAAAA... (20 bytes of 0xAA)
//	numPieces = 1313
//
// The first seven allowed-fast pieces given by BEP 6 are:
//
//	1059, 431, 808, 1217, 287, 376, 1188
//
// http://www.bittorrent.org/beps/bep_0006.html
func TestGenerateFastSet_BEP6Example(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = 0xAA
	}
	want := []uint32{1059, 431, 808, 1217, 287, 376, 1188}
	got := GenerateFastSet(7, 1313, ih, net.IPv4(80, 4, 4, 200))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BEP 6 example mismatch:\nwant %v\n got %v", want, got)
	}
}

// TestGenerateFastSet_Determinism asserts the function is a pure function of
// its inputs. Two calls with identical args must produce byte-identical output.
// Peers rely on this to independently verify the sender's allowed-fast set.
func TestGenerateFastSet_Determinism(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = byte(i)
	}
	ip := net.IPv4(192, 0, 2, 17)
	a := GenerateFastSet(10, 500, ih, ip)
	b := GenerateFastSet(10, 500, ih, ip)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("non-deterministic output:\n#1 %v\n#2 %v", a, b)
	}
}

// TestGenerateFastSet_Subnet24 asserts that IPs in the same /24 yield the
// same fast set (BEP 6 explicitly masks to /24 so a single host can't game
// the set by switching ports).
func TestGenerateFastSet_Subnet24(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = byte(i ^ 0x55)
	}
	a := GenerateFastSet(8, 200, ih, net.IPv4(10, 1, 2, 3))
	b := GenerateFastSet(8, 200, ih, net.IPv4(10, 1, 2, 250))
	c := GenerateFastSet(8, 200, ih, net.IPv4(10, 1, 3, 3)) // different /24
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same /24 produced different sets:\n.3 -> %v\n.250 -> %v", a, b)
	}
	if reflect.DeepEqual(a, c) {
		t.Fatalf("different /24 produced the same set; mask not applied")
	}
}

// TestGenerateFastSet_IPv6Nil — BEP 6 only defines the algorithm for IPv4.
// The function must signal "no fast set" for IPv6-only peers by returning nil.
func TestGenerateFastSet_IPv6Nil(t *testing.T) {
	var ih [20]byte
	ipv6 := net.ParseIP("2001:db8::1")
	got := GenerateFastSet(10, 1000, ih, ipv6)
	if got != nil {
		t.Fatalf("expected nil for IPv6 peer; got %v", got)
	}
}

// TestGenerateFastSet_UniqueIndices — every produced index must be unique.
// The outer/inner loops include a "contains" check; this test confirms it
// holds even with a small piece count where collisions are likely.
func TestGenerateFastSet_UniqueIndices(t *testing.T) {
	var ih [20]byte
	for i := range ih {
		ih[i] = 0xFE
	}
	// Tiny torrent: 7 pieces, ask for 7 → every index must appear at most once.
	got := GenerateFastSet(7, 7, ih, net.IPv4(203, 0, 113, 1))
	seen := map[uint32]bool{}
	for _, v := range got {
		if seen[v] {
			t.Fatalf("duplicate index %d in %v", v, got)
		}
		seen[v] = true
		if v >= 7 {
			t.Fatalf("index %d out of range for numPieces=7", v)
		}
	}
}

// TestGenerateFastSet_EdgeCases — defensive: zero k or zero pieces should
// return nil rather than panic. The caller doesn't have to guard.
func TestGenerateFastSet_EdgeCases(t *testing.T) {
	var ih [20]byte
	ip := net.IPv4(1, 2, 3, 4)
	if got := GenerateFastSet(0, 100, ih, ip); got != nil {
		t.Fatalf("k=0 should return nil, got %v", got)
	}
	if got := GenerateFastSet(5, 0, ih, ip); got != nil {
		t.Fatalf("numPieces=0 should return nil, got %v", got)
	}
}
