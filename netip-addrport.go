package torrent

import (
	"net/netip"

	"github.com/anacrolix/dht/v2/krpc"
)

type addrPorter interface {
	AddrPort() netip.AddrPort
}

func addrPortFromPeerRemoteAddr(pra PeerRemoteAddr) (netip.AddrPort, error) {
	switch v := pra.(type) {
	case addrPorter:
		return v.AddrPort(), nil
	case netip.AddrPort:
		return v, nil
	default:
		return netip.ParseAddrPort(pra.String())
	}
}

func krpcNodeAddrFromAddrPort(addrPort netip.AddrPort) krpc.NodeAddr {
	return krpc.NodeAddr{
		IP:   addrPort.Addr().AsSlice(),
		Port: int(addrPort.Port()),
	}
}
