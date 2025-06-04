package netx

import (
	"context"
	"log"
	"net"
	"net/netip"
	"strconv"

	"github.com/pkg/errors"
)

// Dialer missing interface from the net package.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// NetPort returns the port of the network address,
func NetPort(addr net.Addr) (port int, err error) {
	if addr == nil {
		return 0, errors.New("NetPort: nil net.Addr received")
	}

	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.Port, nil
	case *net.TCPAddr:
		return raw.Port, nil
	default:
		var (
			sport string
			i64   int64
		)

		if _, sport, err = net.SplitHostPort(addr.String()); err != nil {
			return -1, err
		}

		if i64, err = strconv.ParseInt(sport, 0, 0); err != nil {
			return -1, err
		}

		return int(i64), nil
	}
}

// NetIP returns the IP address of the network address.
func NetIP(addr net.Addr) (ip net.IP, err error) {
	if addr == nil {
		return nil, errors.New("NetIP: nil net.Addr received")
	}

	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.IP, nil
	case *net.TCPAddr:
		return raw.IP, nil
	default:
		var (
			host string
		)

		if host, _, err = net.SplitHostPort(addr.String()); err != nil {
			return nil, err
		}

		if ip = net.ParseIP(host); ip == nil {
			return nil, errors.Errorf("invalid IP: %s", host)
		}

		return ip, nil
	}
}

func NetIPOrNil(addr net.Addr) (ip net.IP) {
	ip, err := NetIP(addr)
	if err != nil {
		log.Println(err)
		return nil
	}

	return ip
}

// NetIPPort returns the IP and Port of the network address
func NetIPPort(addr net.Addr) (ip net.IP, port int, err error) {
	if addr == nil {
		return nil, 0, errors.New("NetIPPort: nil net.Addr received")
	}

	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.IP, raw.Port, nil
	case *net.TCPAddr:
		return raw.IP, raw.Port, nil
	default:
		var (
			host  string
			sport string
			i64   int64
		)

		if host, sport, err = net.SplitHostPort(addr.String()); err != nil {
			return nil, -1, err
		}

		if ip = net.ParseIP(host); ip == nil {
			return nil, -1, errors.Errorf("invalid IP: %s", host)
		}

		if i64, err = strconv.ParseInt(sport, 0, 0); err != nil {
			return nil, -1, err
		}

		return ip, int(i64), nil
	}
}

func AddrPort(addr net.Addr) (_ netip.AddrPort, err error) {
	if addr == nil {
		return netip.AddrPort{}, errors.New("NetIPPort: nil net.Addr received")
	}

	switch raw := addr.(type) {
	case *net.UDPAddr:
		return raw.AddrPort(), nil
	case *net.TCPAddr:
		return raw.AddrPort(), nil
	default:
		return netip.ParseAddrPort(addr.String())
	}
}

func AddrFromIP(ip net.IP) netip.Addr {
	if ip == nil {
		return netip.IPv6Unspecified().Unmap()
	}
	// log.Println("DERP DERP", ip, netip.Addr{}.IsValid())
	return netip.AddrFrom16([16]byte(ip.To16())).Unmap()
}

func FirstAddrOrZero(addrs ...netip.Addr) netip.Addr {
	for _, a := range addrs {
		if a.IsValid() {
			return a
		}
	}

	return netip.Addr{}
}
