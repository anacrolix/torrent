// Package fast implements the Allowed Fast Set generation algorithm from
// BEP 6 (Fast Extension).
//
// http://www.bittorrent.org/beps/bep_0006.html
//
// The algorithm is a deterministic function of the peer's IPv4 (masked to /24)
// and the torrent's info hash, producing a small set of piece indices the
// local peer is willing to serve while the remote peer is choked. The remote
// peer can independently compute the same set to verify the sender is honest.
package fast

import (
	"crypto/sha1"
	"encoding/binary"
	"net"
	"slices"
)

// DefaultK is the number of allowed-fast pieces normally announced per peer.
// BEP 6 leaves the exact value implementation-defined; common clients use a
// small constant (mainline / libtorrent use 10).
const DefaultK = 10

// GenerateFastSet returns up to k piece indices that the local peer will
// allow the remote peer to request even while choked.
//
// k is the requested set size, numPieces is the total number of pieces in
// the torrent, infoHash is the torrent's 20-byte info hash, and ip is the
// remote peer's address (IPv4 only — IPv6 returns nil per BEP 6).
//
// The algorithm follows BEP 6's reference pseudocode exactly so the remote
// peer can independently re-derive and verify the set.
func GenerateFastSet(k int, numPieces uint32, infoHash [20]byte, ip net.IP) []uint32 {
	if k <= 0 || numPieces == 0 {
		return nil
	}
	// BEP 6 only defines the algorithm for IPv4. IPv6 callers get nil.
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	// Mask off the host bits, keeping the /24 network. This makes the set
	// stable across reconnects from the same subnet, which is what the spec
	// requires.
	ip4 = ip4.Mask(net.CIDRMask(24, 32))

	// x = masked_ip || infohash (4 + 20 = 24 bytes)
	x := make([]byte, 24)
	copy(x, ip4)
	copy(x[4:], infoHash[:])

	out := make([]uint32, 0, k)
	contains := func(v uint32) bool { return slices.Contains(out, v) }

	h := sha1.New()
	// Outer cap mirrors the reference pseudocode: it bounds work for the
	// pathological case where numPieces is tiny and many hash outputs collide.
	for j := 0; j < k && len(out) < k; j++ {
		h.Reset()
		_, _ = h.Write(x)
		x = h.Sum(x[:0])
		for i := 0; i+4 <= 20 && len(out) < k; i += 4 {
			y := binary.BigEndian.Uint32(x[i : i+4])
			idx := y % numPieces
			if !contains(idx) {
				out = append(out, idx)
			}
		}
	}
	return out
}
