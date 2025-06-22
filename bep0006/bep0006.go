package bep0006

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"net/netip"

	"github.com/RoaringBitmap/roaring/v2"
)

// AllowedFastSet computes the allowed fast set for a peer.
// This algorithm is described in BitTorrent Enhancement Proposal 6 (BEP 6): Fast Extension.
//
// ip: P's IPv4 address (4 bytes). This should be the externally facing IP address
//
//	of the peer P' for which the allowed fast set is being generated.
//
// infoHash: The torrent's infohash (20 bytes). This is the SHA1 hash of the
//
//	info dictionary from the .torrent file.
//
// numPieces: The total number of pieces in the torrent (sz in pseudocode).
// k: The final desired number of pieces in the allowed fast set.
//
// Returns a sorted slice of integers representing the piece indices in the
// allowed fast set, and an error if input validation fails.
func AllowedFastSet(ip netip.Addr, infoHash [20]byte, numPieces uint64, k uint64) (*roaring.Bitmap, error) {
	if numPieces == 0 {
		return roaring.NewBitmap(), errors.New("numPieces cannot be zero")
	}

	if k > numPieces {
		return roaring.NewBitmap(), errors.New("k cannot be greater than numPieces")
	}

	// Validate input parameters
	if k == 0 {
		return roaring.NewBitmap(), nil // If k is 0, return an empty set
	}

	// allowedFastSet will store the unique piece indices.
	allowedFastSet := roaring.NewBitmap()

	// Step (1): x = 0xFFFFFF00 & ip
	// This operation conceptually zeros out the last byte of the IPv4 address.
	// Create a new 4-byte slice for the masked IP.
	ipb := ip.AsSlice()[:4]
	maskedIP := make([]byte, 4)
	copy(maskedIP, ipb[:])
	maskedIP[3] = 0x00 // Set the last byte (octet) to 0x00

	// Step (2): x.append(infohash)
	// Initialize currentX with the concatenation of the masked IP and the infohash.
	// This becomes the initial input for the SHA1 hash function in the loop.
	currentX := make([]byte, 0, len(maskedIP)+len(infoHash)) // Pre-allocate capacity
	currentX = append(currentX, maskedIP...)
	currentX = append(currentX, infoHash[:]...)

	// The main loop continues until 'k' unique piece indices are found
	for allowedFastSet.GetCardinality() < k {
		// Step (3): x = SHA1(x)
		// Compute the SHA1 hash of the current 'x'. SHA1 produces a 20-byte hash.
		hasher := sha1.New()
		hasher.Write(currentX)
		currentX = hasher.Sum(nil) // 'Sum(nil)' appends the hash to a new slice.
		// 'currentX' now holds the 20-byte SHA1 hash.

		// Step (4): for i in [0:5] and |a| < k:
		// This inner loop iterates 5 times (for i = 0, 1, 2, 3, 4) or until 'k' pieces are found.
		for i := 0; i < 5 && allowedFastSet.GetCardinality() < k; i++ {
			// Step (5): j = i*4
			// Calculate the starting byte index within the 20-byte SHA1 hash (currentX).
			j := i * 4

			// Step (6): y = x[j:j+4]
			// Extract a 4-byte (32-bit) subsequence from the SHA1 hash.
			// This sub-sequence will be used to derive a piece index.
			// Safety check: ensure we don't go out of bounds. SHA1 always returns 20 bytes,
			// and j+4 will not exceed 20 for i up to 4 (e.g., max j=16, j+4=20).
			if j+4 > len(currentX) {
				// This case should ideally not be reached under normal SHA1 behavior,
				// but it's a good practice for robustness in case of unexpected input or behavior.
				break
			}
			y := currentX[j : j+4]

			// Step (7): index = y % sz
			// Convert the 4-byte 'y' (which is in network/big-endian byte order) to a
			// 32-bit unsigned integer. Then, compute the modulo of this value by
			// the total number of pieces (numPieces) to get a valid piece index.
			val := binary.BigEndian.Uint32(y)
			index := val % uint32(numPieces)

			// Step (8) & (9): if index not in a: add index to a
			// Check if the generated index is already in our set of allowed fast pieces.
			// If it's a new unique index, add it to the set.
			allowedFastSet.Add(index)
		}
	}

	return allowedFastSet, nil
}
