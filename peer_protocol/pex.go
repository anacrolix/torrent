package peer_protocol

import (
	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

// PexMsg http://bittorrent.org/beps/bep_0011.html
type PexMsg struct {
	Added       krpc.CompactIPv4NodeAddrs `bencode:"added"`
	AddedFlags  []PexPeerFlags            `bencode:"added.f"`
	Added6      krpc.CompactIPv6NodeAddrs `bencode:"added6"`
	Added6Flags []PexPeerFlags            `bencode:"added6.f"`
	Dropped     krpc.CompactIPv4NodeAddrs `bencode:"dropped"`
	Dropped6    krpc.CompactIPv6NodeAddrs `bencode:"dropped6"`
}

// Message describing the peer exchange.
func (t *PexMsg) Message(eid ExtensionNumber) Message {
	payload := bencode.MustMarshal(t)
	return Message{
		Type:            Extended,
		ExtendedID:      eid,
		ExtendedPayload: payload,
	}
}

// PexPeerFlags flags describing peers supported functionality.
type PexPeerFlags byte

// Get checks if the provided flags are set.
func (t PexPeerFlags) Get(f PexPeerFlags) bool {
	return t&f == f
}

// Constants for PEX messages.
const (
	PexPrefersEncryption = 0x01
	PexSeedUploadOnly    = 0x02
	PexSupportsUtp       = 0x04
	PexHolepunchSupport  = 0x08
	PexOutgoingConn      = 0x10
)
