package torrent

import (
	"net"
	"strconv"

	"github.com/anacrolix/missinggo"
)

type ipPort struct {
	IP   net.IP
	Port uint16
}

func (me ipPort) String() string {
	return net.JoinHostPort(me.IP.String(), strconv.FormatUint(uint64(me.Port), 10))
}

func ipPortFromNetAddr(na net.Addr) ipPort {
	return ipPort{missinggo.AddrIP(na), uint16(missinggo.AddrPort(na))}
}
