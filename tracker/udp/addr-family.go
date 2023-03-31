package udp

import (
	"encoding"

	"github.com/anacrolix/dht/v2/krpc"
)

// Discriminates behaviours based on address family in use.
type AddrFamily int

const (
	AddrFamilyIpv4 = iota + 1
	AddrFamilyIpv6
)

// Returns a marshaler for the given node addrs for the specified family.
func GetNodeAddrsCompactMarshaler(nas []krpc.NodeAddr, family AddrFamily) encoding.BinaryMarshaler {
	switch family {
	case AddrFamilyIpv4:
		return krpc.CompactIPv4NodeAddrs(nas)
	case AddrFamilyIpv6:
		return krpc.CompactIPv6NodeAddrs(nas)
	}
	return nil
}
