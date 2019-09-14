package torrent

import (
	"net"
	"strings"
)

func allPeerNetworks() (ret []network) {
	for _, s := range []string{"tcp", "udp"} {
		ret = append(ret, parseNetworkString(s))
	}
	return
}

type network struct {
	Udp bool
	Tcp bool
}

func (n network) String() (ret string) {
	a := func(b bool, s string) {
		if b {
			ret += s
		}
	}
	a(n.Udp, "udp")
	a(n.Tcp, "tcp")
	return
}

func parseNetworkString(network string) (ret network) {
	c := func(s string) bool {
		return strings.Contains(network, s)
	}

	ret.Udp = c("udp")
	ret.Tcp = c("tcp")
	return
}

func parseIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP
	case *net.UDPAddr:
		return a.IP
	default:
		return nil
	}
}

func peerNetworkEnabled(a net.Addr, cfg *ClientConfig) bool {
	n := parseNetworkString(a.Network())
	ip := parseIP(a)
	if cfg.DisableUTP && n.Udp {
		return false
	}
	if cfg.DisableTCP && n.Tcp {
		return false
	}
	if cfg.DisableIPv6 && len(ip) == net.IPv6len && ip.To4() == nil {
		return false
	}
	if cfg.DisableIPv4 && len(ip) == net.IPv4len {
		return false
	}
	return true
}
