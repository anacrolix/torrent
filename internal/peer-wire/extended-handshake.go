package peerWire

import (
	"sync/atomic"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// ExtendedHandshake is what the peer has told us in its BEP 10 extended handshakes. The fields are
// exported because they're promoted into torrent.PeerConn's public API. Update it with
// RecvExtendedHandshake.
type ExtendedHandshake struct {
	// The peer's extension map, as sent in their extended handshake.
	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	// The 'v' field from the extended handshake, as a string.
	PeerClientName atomic.Value
	// The 'p' field from the extended handshake.
	PeerListenPort int
	// As indicated by 'e' field in extension handshake.
	PeerPrefersEncryption bool
	// Maximum pending requests the peer allows. The 'reqq' field from the extended handshake if
	// given, otherwise a default chosen by the caller.
	PeerMaxRequests int
}

// RecvExtendedHandshake applies an extended handshake to h. The handshake can be repeated, and
// later ones update earlier ones. Extension names are peer-controlled, so at most maxNames are
// tracked. It returns the names seen for the first time. metadata_size isn't handled here because
// the caller validates it before calling State.SetPeerMetadataSize.
func RecvExtendedHandshake(
	h *ExtendedHandshake,
	d *pp.ExtendedHandshakeMessage,
	maxNames int,
) (newNames []pp.ExtensionName) {
	if d.Reqq != 0 {
		h.PeerMaxRequests = d.Reqq
	}
	h.PeerClientName.Store(d.V)
	if h.PeerExtensionIDs == nil {
		h.PeerExtensionIDs = make(map[pp.ExtensionName]pp.ExtensionNumber, len(d.M))
	}
	h.PeerListenPort = d.Port
	h.PeerPrefersEncryption = d.Encryption
	for name, id := range d.M {
		if _, ok := h.PeerExtensionIDs[name]; !ok {
			if len(h.PeerExtensionIDs) >= maxNames {
				continue
			}
			newNames = append(newNames, name)
		}
		h.PeerExtensionIDs[name] = id
	}
	return
}
