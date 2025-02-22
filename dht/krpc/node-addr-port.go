package krpc

import (
	"net"
	"net/netip"

	"github.com/anacrolix/multiless"
)

// This is a comparable replacement for NodeAddr.
type NodeAddrPort struct {
	netip.AddrPort
}

func (me NodeAddrPort) ToNodeAddr() NodeAddr {
	return NodeAddr{me.Addr().AsSlice(), int(me.Port())}
}

func (me NodeAddrPort) UDP() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   me.Addr().AsSlice(),
		Port: int(me.Port()),
	}
}

func (me NodeAddrPort) IP() net.IP {
	return me.Addr().AsSlice()
}

func (l NodeAddrPort) Compare(r NodeAddrPort) int {
	return multiless.EagerOrdered(
		multiless.New().Cmp(l.Addr().Compare(r.Addr())),
		l.Port(), r.Port(),
	).OrderingInt()
}
