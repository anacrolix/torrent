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

func addrEqual(a, b *krpc.NodeAddr) bool {
	return a.IP.Equal(b.IP) && a.Port == b.Port
}

func addrIndex(v []krpc.NodeAddr, a *krpc.NodeAddr) int {
	for i := range v {
		if addrEqual(&v[i], a) {
			return i
		}
	}
	return -1
}

func (m *PexMsg) Add(addr krpc.NodeAddr, f PexPeerFlags) {
	if addr.IP.To4() != nil {
		if addrIndex(m.Added.NodeAddrs(), &addr) >= 0 {
			// already added
			return
		}
		if i := addrIndex(m.Dropped.NodeAddrs(), &addr); i >= 0 {
			// on the dropped list - cancel out
			m.Dropped = append(m.Dropped[:i], m.Dropped[i+1:]...)
			return
		}
		m.Added = append(m.Added, addr)
		m.AddedFlags = append(m.AddedFlags, f)
	} else if len(addr.IP) == net.IPv6len {
		if addrIndex(m.Added6.NodeAddrs(), &addr) >= 0 {
			// already added
			return
		}
		if i := addrIndex(m.Dropped6.NodeAddrs(), &addr); i >= 0 {
			// on the dropped list - cancel out
			m.Dropped6 = append(m.Dropped6[:i], m.Dropped6[i+1:]...)
			return
		}
		m.Added6 = append(m.Added6, addr)
		m.Added6Flags = append(m.Added6Flags, f)
	}
}

func (m *PexMsg) Drop(addr krpc.NodeAddr) {
	if addr.IP.To4() != nil {
		if addrIndex(m.Dropped.NodeAddrs(), &addr) >= 0 {
			// already dropped
			return
		}
		if i := addrIndex(m.Added.NodeAddrs(), &addr); i >= 0 {
			// on the added list - cancel out
			m.Added = append(m.Added[:i], m.Added[i+1:]...)
			m.AddedFlags = append(m.AddedFlags[:i], m.AddedFlags[i+1:]...)
			return
		}
		m.Dropped = append(m.Dropped, addr)
	} else if len(addr.IP) == net.IPv6len {
		if addrIndex(m.Dropped6.NodeAddrs(), &addr) >= 0 {
			// already dropped
			return
		}
		if i := addrIndex(m.Added6.NodeAddrs(), &addr); i >= 0 {
			// on the added list - cancel out
			m.Added6 = append(m.Added6[:i], m.Added6[i+1:]...)
			m.Added6Flags = append(m.Added6Flags[:i], m.Added6Flags[i+1:]...)
			return
		}
		m.Dropped6 = append(m.Dropped6, addr)
	}
}

func (m *PexMsg) Len() int {
	return len(m.Added)+len(m.Added6)+len(m.Dropped)+len(m.Dropped6)
}

// DeltaLen returns max of {added+added6, dropped+dropped6}
func (m *PexMsg) DeltaLen() int {
	lenAdded := len(m.Added)+len(m.Added6)
	lenDropped := len(m.Dropped)+len(m.Dropped6)
	if lenAdded > lenDropped {
		return lenAdded
	}
	return lenDropped
}

func (m *PexMsg) Message(pexExtendedId ExtensionNumber) Message {
	payload := bencode.MustMarshal(m)
	return Message{
		Type:            Extended,
		ExtendedID:      pexExtendedId,
		ExtendedPayload: payload,
	}
}

func LoadPexMsg(b []byte) (*PexMsg, error) {
	m := new(PexMsg) 
	if err := bencode.Unmarshal(b, m); err != nil {
		return nil, err
	}
	return m, nil
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
