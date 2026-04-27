package merkle

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestHashSumMatchesManualBlockRoot(t *testing.T) {
	data := makeTestData(BlockSize*2 + BlockSize/2)
	h := NewHash()
	writeInChunks(t, h, data, 1, 17, BlockSize-3, 23)
	got := h.Sum(nil)
	want := RootWithPadHash(blockHashesFromData(data), [sha256.Size]byte{})
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestHashSumMinLengthPadsMissingBlocks(t *testing.T) {
	data := makeTestData(BlockSize + 123)
	h := NewHash()
	writeInChunks(t, h, data, 257, BlockSize-1, 11)
	minLength := BlockSize * 4
	got := h.SumMinLength(nil, minLength)

	blocks := blockHashesFromData(data)
	blocks = append(blocks, make([][sha256.Size]byte, blocksForLength(minLength)-len(blocks))...)
	want := RootWithPadHash(blocks, [sha256.Size]byte{})
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestHashResetClearsState(t *testing.T) {
	h := NewHash()
	writeInChunks(t, h, makeTestData(BlockSize+99), 33, BlockSize-5)
	h.Reset()
	if h.nextBlockWritten != 0 {
		t.Fatalf("nextBlockWritten = %d, want 0", h.nextBlockWritten)
	}
	if len(h.blocks) != 0 {
		t.Fatalf("len(blocks) = %d, want 0", len(h.blocks))
	}
	got := h.Sum(nil)
	want := sha256.Sum256(nil)
	if !bytes.Equal(got, want[:]) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func BenchmarkHashWriteAndSum(b *testing.B) {
	data := makeTestData(BlockSize * 16)
	b.ReportAllocs()
	for b.Loop() {
		h := NewHash()
		if _, err := h.Write(data); err != nil {
			b.Fatalf("write failed: %v", err)
		}
		_ = h.Sum(nil)
	}
}

func makeTestData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i * 31)
	}
	return data
}

func writeInChunks(t testing.TB, h *Hash, data []byte, chunkSizes ...int) {
	t.Helper()
	offset := 0
	chunk := 0
	for offset < len(data) {
		size := chunkSizes[chunk%len(chunkSizes)]
		if size <= 0 {
			t.Fatalf("invalid chunk size %d", size)
		}
		end := min(offset+size, len(data))
		n, err := h.Write(data[offset:end])
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if n != end-offset {
			t.Fatalf("write returned %d, want %d", n, end-offset)
		}
		offset = end
		chunk++
	}
}

func blockHashesFromData(data []byte) [][sha256.Size]byte {
	if len(data) == 0 {
		return nil
	}
	blocks := make([][sha256.Size]byte, 0, blocksForLength(len(data)))
	for offset := 0; offset < len(data); offset += BlockSize {
		end := min(offset+BlockSize, len(data))
		blocks = append(blocks, sha256.Sum256(data[offset:end]))
	}
	return blocks
}

func blocksForLength(length int) int {
	return (length + BlockSize - 1) / BlockSize
}
