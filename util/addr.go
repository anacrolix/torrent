package util

import (
	"net"
	"strconv"
)

// Extracts the port as an integer from an address string.
func AddrPort(addr net.Addr) int {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		panic(err)
	}
	i64, err := strconv.ParseInt(port, 0, 0)
	if err != nil {
		panic(err)
	}
	return int(i64)
}

func AddrIP(addr net.Addr) net.IP {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		panic(err)
	}
	return net.ParseIP(host)
}
