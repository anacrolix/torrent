package peerWire

import (
	"bytes"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

// Handshake is what the peer told us in its BEP 3 handshake. The fields are exported because
// they're promoted into torrent.PeerConn's public API. Set it with RecvHandshake.
type Handshake struct {
	PeerID             types.PeerID
	PeerExtensionBytes pp.PeerExtensionBits
}

// RecvHandshake records the peer's half of a completed handshake.
func RecvHandshake(h *Handshake, res pp.HandshakeResult) {
	h.PeerExtensionBytes = res.PeerExtensionBits
	h.PeerID = res.PeerID
}

// MeetsMinExtensions returns whether have sets every bit set in min.
func MeetsMinExtensions(have, min pp.PeerExtensionBits) bool {
	for i, b := range min {
		if have[i]&b != b {
			return false
		}
	}
	return true
}

// IsTransmission returns whether the peer ID uses Transmission's -TRxyzb- scheme
// (https://github.com/transmission/transmission/blob/7ec7607bbcf0fa99bd4b157b9b0f0c411d59f45d/CMakeLists.txt#L128-L149).
func IsTransmission(id types.PeerID) bool {
	return bytes.HasPrefix(id[:], []byte("-TR")) && id[7] == '-'
}

// RejectsCancels returns whether a peer with the fast extension enabled answers our Cancel with a
// Reject or the Piece, as BEP 6 requires. Transmission before 4.0.0 didn't
// (https://github.com/transmission/transmission/pull/2275).
func RejectsCancels(id types.PeerID, fastEnabled bool) bool {
	if !fastEnabled {
		return false
	}
	if IsTransmission(id) {
		return id[3] >= '4'
	}
	return true
}

// UsesOwnExtensionIDs returns whether the peer ID belongs to a client known to send extended
// messages with its own extension IDs rather than ours (BEP 10), so they can't be decoded.
func UsesOwnExtensionIDs(id types.PeerID) bool {
	return bytes.HasPrefix(id[:], []byte("-SD0100-")) || bytes.HasPrefix(id[:], []byte("-XL0012-"))
}
