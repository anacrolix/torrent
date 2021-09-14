package torrent

import (
	"net"
	"strconv"
)

// Extracts the port as an integer from an address string.
func addrPortOrZero(addr net.Addr) int {
	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.Port
	case *net.TCPAddr:
		return raw.Port
	default:
		_, port, err := net.SplitHostPort(addr.String())
		if err != nil {
			return 0
		}
		i64, err := strconv.ParseUint(port, 0, 16)
		if err != nil {
			panic(err)
		}
		return int(i64)
	}
}

func addrIpOrNil(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.IP
	case *net.TCPAddr:
		return raw.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return nil
		}
		return net.ParseIP(host)
	}
}

type ipPortAddr struct {
	IP   net.IP
	Port int
}

func (ipPortAddr) Network() (s string) {
	return
}

func (me ipPortAddr) String() string {
	return net.JoinHostPort(me.IP.String(), strconv.FormatUint(uint64(me.Port), 10))
}

func tryIpPortFromNetAddr(addr PeerRemoteAddr) (ipa ipPortAddr, ok bool) {
	if host, port, err := net.SplitHostPort(addr.String()); err == nil {
		ipa.IP = net.ParseIP(host)
		if portI64, err := strconv.ParseUint(port, 10, 0); err == nil {
			ipa.Port = int(portI64)
			return ipa, true
		}
	}
	return
}

