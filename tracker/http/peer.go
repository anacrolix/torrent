package httpTracker

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/anacrolix/dht/v2/krpc"
)

// TODO: Use netip.Addr and Option[[20]byte].
type Peer struct {
	IP   net.IP `bencode:"ip"`
	Port int    `bencode:"port"`
	ID   []byte `bencode:"peer id"`
}

func (p Peer) ToNetipAddrPort() (addrPort netip.AddrPort, ok bool) {
	addr, ok := netip.AddrFromSlice(p.IP)
	addrPort = netip.AddrPortFrom(addr, uint16(p.Port))
	return
}

func (p Peer) String() string {
	loc := net.JoinHostPort(p.IP.String(), fmt.Sprintf("%d", p.Port))
	if len(p.ID) != 0 {
		return fmt.Sprintf("%x at %s", p.ID, loc)
	} else {
		return loc
	}
}

// Set from the non-compact form in BEP 3.
func (p *Peer) FromDictInterface(d map[string]interface{}) {
	p.IP = net.ParseIP(d["ip"].(string))
	if _, ok := d["peer id"]; ok {
		p.ID = []byte(d["peer id"].(string))
	}
	p.Port = int(d["port"].(int64))
}

func (p Peer) FromNodeAddr(na krpc.NodeAddr) Peer {
	p.IP = na.IP
	p.Port = na.Port
	return p
}
