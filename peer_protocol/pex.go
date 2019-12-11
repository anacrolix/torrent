package peer_protocol

import (
	"net"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/bencode"
)

type PexMsg struct {
	Added       krpc.CompactIPv4NodeAddrs `bencode:"added"`
	AddedFlags  []PexPeerFlags            `bencode:"added.f"`
	Added6      krpc.CompactIPv6NodeAddrs `bencode:"added6"`
	Added6Flags []PexPeerFlags            `bencode:"added6.f"`
	Dropped     krpc.CompactIPv4NodeAddrs `bencode:"dropped"`
	Dropped6    krpc.CompactIPv6NodeAddrs `bencode:"dropped6"`
}

func (m *PexMsg) AppendAdded(addr krpc.NodeAddr, f PexPeerFlags) {
	ip := addr.IP
	if ip.To4() != nil {
		m.Added = append(m.Added, addr)
		m.AddedFlags = append(m.AddedFlags, f)
	} else if len(ip) == net.IPv6len {
		m.Added6 = append(m.Added6, addr)
		m.Added6Flags = append(m.Added6Flags, f)
	}
}

func (m *PexMsg) AppendDropped(addr krpc.NodeAddr) {
	ip := addr.IP
	if ip.To4() != nil {
		m.Dropped = append(m.Dropped, addr)
	} else if len(ip) == net.IPv6len {
		m.Dropped6 = append(m.Dropped6, addr)
	}
}

func (pexMsg *PexMsg) Message(pexExtendedId ExtensionNumber) Message {
	payload := bencode.MustMarshal(pexMsg)
	return Message{
		Type:            Extended,
		ExtendedID:      pexExtendedId,
		ExtendedPayload: payload,
	}
}

type PexPeerFlags byte

func (me PexPeerFlags) Get(f PexPeerFlags) bool {
	return me&f == f
}

const (
	PexPrefersEncryption PexPeerFlags = 1 << iota
	PexSeedUploadOnly
	PexSupportsUtp
	PexHolepunchSupport
	PexOutgoingConn
)
