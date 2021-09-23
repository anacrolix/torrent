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

func parseNetworkString(netw string) (ret network) {
	switch netw {
	case "udp4":
		return network{Udp: true, Ipv4: true}
	case "udp6":
		return network{Udp: true, Ipv6: true}
	case "tcp4":
		return network{Tcp: true, Ipv4: true}
	case "tcp6":
		return network{Tcp: true, Ipv6: true}
	case "udp":
		return network{Udp: true}
	case "tcp":
		return network{Tcp: true}
	}
	panic("")
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
