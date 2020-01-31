package torrent

import "strings"

var allPeerNetworks = func() (ret []network) {
	for _, s := range []string{"tcp4", "tcp6", "udp4", "udp6"} {
		ret = append(ret, parseNetworkString(s))
	}
	return
}()

type network struct {
	Ipv4 bool
	Ipv6 bool
	UDP  bool
	TCP  bool
}

func (n network) String() (ret string) {
	a := func(b bool, s string) {
		if b {
			ret += s
		}
	}
	a(n.UDP, "udp")
	a(n.TCP, "tcp")
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
	ret.UDP = c("udp")
	ret.TCP = c("tcp")
	return
}

func peerNetworkEnabled(n network, cfg *ClientConfig) bool {
	if cfg.DisableUTP && n.UDP {
		return false
	}
	if cfg.DisableTCP && n.TCP {
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
