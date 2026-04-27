package merkle

import (
	"crypto/sha256"
	"fmt"
	"math/bits"
)

// The leaf block size for BitTorrent v2 Merkle trees.
const BlockSize = 1 << 14 // 16KiB

func Root(hashes [][sha256.Size]byte) [sha256.Size]byte {
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
	working := make([][sha256.Size]byte, len(hashes))
	copy(working, hashes)
	return rootInPlace(working)
}

func RootWithPadHash(hashes [][sha256.Size]byte, padHash [sha256.Size]byte) [sha256.Size]byte {
	switch len(hashes) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return hashes[0]
	}
	targetLen := len(hashes)
	if !isPowerOfTwo(targetLen) {
		targetLen = int(RoundUpToPowerOfTwo(uint(targetLen)))
	}
	working := make([][sha256.Size]byte, targetLen)
	copy(working, hashes)
	for i := len(hashes); i < targetLen; i++ {
		working[i] = padHash
	}
	return rootInPlace(working)
}

func CompactLayerToSliceHashes(compactLayer string) (hashes [][sha256.Size]byte, err error) {
	hashes = make([][sha256.Size]byte, len(compactLayer)/sha256.Size)
	for i := range hashes {
		copy(hashes[i][:], compactLayer[i*sha256.Size:(i+1)*sha256.Size])
	}
	return
}

// rootInPlace reduces a complete hash layer to its root by overwriting the slice level by level.
func rootInPlace(level [][sha256.Size]byte) [sha256.Size]byte {
	var pair [2 * sha256.Size]byte
	for len(level) > 1 {
		nextLen := len(level) / 2
		for i := range nextLen {
			left := level[i*2]
			right := level[i*2+1]
			copy(pair[:sha256.Size], left[:])
			copy(pair[sha256.Size:], right[:])
			level[i] = sha256.Sum256(pair[:])
		}
		level = level[:nextLen]
	}
	return level[0]
}

// isPowerOfTwo reports whether n is a positive power of two.
func isPowerOfTwo(n int) bool {
	return n > 0 && n&(n-1) == 0
}

func RoundUpToPowerOfTwo(n uint) (ret uint) {
	return 1 << bits.Len(n-1)
}

func Log2RoundingUp(n uint) (ret uint) {
	return uint(bits.Len(n - 1))
}
