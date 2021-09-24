package torrent

import "strings"

var allPeerNetworks = []network{
	{Tcp: true, Ipv4: true},
	{Tcp: true, Ipv6: true},
	{Udp: true, Ipv4: true},
	{Udp: true, Ipv6: true},
}

type network struct {
	Ipv4 bool
	Ipv6 bool
	Udp  bool
	Tcp  bool
}

func (n network) String() (ret string) {
	a := func(b bool, s string) {
		if b {
			ret += s
		}
	}
	a(n.Udp, "udp")
	a(n.Tcp, "tcp")
	a(n.Ipv4, "4")
	a(n.Ipv6, "6")
	return
}

func parseNetworkString(network string) (ret network) {
	c := func(s string) bool {
		return strings.Contains(network, s)
	}
	ret.Ipv4 = c("4")
	ret.Ipv6 = c("6")
	ret.Udp = c("udp")
	ret.Tcp = c("tcp")
	return
}

func peerNetworkEnabled(n network, cfg *ClientConfig) bool {
	if cfg.DisableUTP && n.Udp {
		return false
	}
	if cfg.DisableTCP && n.Tcp {
		return false
	}
	if cfg.DisableIPv6 && n.Ipv6 {
		return false
	}
	if cfg.DisableIPv4 && n.Ipv4 {
		return false
	}
	return true
}
