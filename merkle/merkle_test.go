package merkle

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"testing"
)

func TestRootMatchesReference(t *testing.T) {
	for _, leaves := range []int{0, 1, 2, 4, 8, 64, 1024} {
		t.Run(fmt.Sprintf("leaves=%d", leaves), func(t *testing.T) {
			hashes := makeTestHashes(leaves)
			got := Root(hashes)
			want := referenceRoot(hashes)
			if got != want {
				t.Fatalf("got %x, want %x", got, want)
			}
		})
	}
}

func TestRootWithPadHashMatchesReference(t *testing.T) {
	padHash := sha256.Sum256([]byte("pad"))
	for _, leaves := range []int{0, 1, 3, 5, 63, 511} {
		t.Run(fmt.Sprintf("leaves=%d", leaves), func(t *testing.T) {
			hashes := makeTestHashes(leaves)
			got := RootWithPadHash(hashes, padHash)
			want := referenceRootWithPadHash(hashes, padHash)
			if got != want {
				t.Fatalf("got %x, want %x", got, want)
			}
		})
	}
}

func TestRootDoesNotMutateInput(t *testing.T) {
	hashes := makeTestHashes(8)
	original := append([][sha256.Size]byte(nil), hashes...)
	_ = Root(hashes)
	if len(hashes) != len(original) {
		t.Fatalf("input length changed from %d to %d", len(original), len(hashes))
	}
	for i := range hashes {
		if hashes[i] != original[i] {
			t.Fatalf("input hash at index %d changed", i)
		}
	}
}

func TestRootPanicsForNonPowerOfTwo(t *testing.T) {
	hashes := makeTestHashes(3)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for non-power-of-two hash count")
		}
	}()
	_ = Root(hashes)
}

func TestCompactLayerToSliceHashes(t *testing.T) {
	hashes := makeTestHashes(4)
	var compact []byte
	for _, hash := range hashes {
		compact = append(compact, hash[:]...)
	}
	got, err := CompactLayerToSliceHashes(string(compact))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(got, hashes) {
		t.Fatalf("got %x, want %x", got, hashes)
	}
}

func BenchmarkRoot(b *testing.B) {
	hashes := makeTestHashes(1024)
	b.ReportAllocs()
	for b.Loop() {
		_ = Root(hashes)
	}
}

func BenchmarkRootWithPadHash(b *testing.B) {
	hashes := makeTestHashes(511)
	padHash := sha256.Sum256([]byte("pad"))
	b.ReportAllocs()
	for b.Loop() {
		_ = RootWithPadHash(hashes, padHash)
	}
}

func makeTestHashes(n int) [][sha256.Size]byte {
	hashes := make([][sha256.Size]byte, n)
	for i := range hashes {
		hashes[i] = sha256.Sum256([]byte(fmt.Sprintf("leaf-%d", i)))
	}
	return hashes
}

func referenceRoot(hashes [][sha256.Size]byte) [sha256.Size]byte {
	switch len(hashes) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return hashes[0]
	}
	numHashes := uint(len(hashes))
	if numHashes != RoundUpToPowerOfTwo(uint(len(hashes))) {
		panic(fmt.Sprintf("expected power of two number of hashes, got %d", numHashes))
	}
	var next [][sha256.Size]byte
	for i := 0; i < len(hashes); i += 2 {
		left := hashes[i]
		right := hashes[i+1]
		next = append(next, sha256.Sum256(append(left[:], right[:]...)))
	}
	return referenceRoot(next)
}

func referenceRootWithPadHash(hashes [][sha256.Size]byte, padHash [sha256.Size]byte) [sha256.Size]byte {
	for uint(len(hashes)) < RoundUpToPowerOfTwo(uint(len(hashes))) {
		hashes = append(hashes, padHash)
	}
	return referenceRoot(hashes)
}
