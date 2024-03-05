package merkle

import (
	"crypto/sha256"
	"fmt"
	"math/bits"

	g "github.com/anacrolix/generics"
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
	var next [][sha256.Size]byte
	for i := 0; i < len(hashes); i += 2 {
		left := hashes[i]
		right := hashes[i+1]
		h := sha256.Sum256(append(left[:], right[:]...))
		next = append(next, h)
	}
	return Root(next)
}

func RootWithPadHash(hashes [][sha256.Size]byte, padHash [sha256.Size]byte) [sha256.Size]byte {
	for uint(len(hashes)) < RoundUpToPowerOfTwo(uint(len(hashes))) {
		hashes = append(hashes, padHash)
	}
	return Root(hashes)
}

func CompactLayerToSliceHashes(compactLayer string) (hashes [][sha256.Size]byte, err error) {
	g.MakeSliceWithLength(&hashes, len(compactLayer)/sha256.Size)
	for i := range hashes {
		n := copy(hashes[i][:], compactLayer[i*sha256.Size:])
		if n != sha256.Size {
			err = fmt.Errorf("compact layer has incomplete hash at index %d", i)
			return
		}
	}
	return
}

func RoundUpToPowerOfTwo(n uint) (ret uint) {
	return 1 << bits.Len(n-1)
}

func Log2RoundingUp(n uint) (ret uint) {
	return uint(bits.Len(n - 1))
}
