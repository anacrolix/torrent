package httpTracker

import (
	"errors"
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

// Package-level errors for malformed peer dictionaries. We use these instead of
// fmt.Errorf to avoid allocating a new error string on every malformed entry,
// since a hostile tracker could send many of them in a single response.
var (
	errPeerDictMissingIP   = errors.New("peer dict: missing or invalid \"ip\"")
	errPeerDictMissingPort = errors.New("peer dict: missing or invalid \"port\"")
)

// Set from the non-compact form in BEP 3. Returns an error if required fields
// are missing or have unexpected types.
func (p *Peer) FromDictInterface(d map[string]interface{}) error {
	ipStr, ok := d["ip"].(string)
	if !ok {
		return errPeerDictMissingIP
	}
	p.IP = net.ParseIP(ipStr)
	if p.IP == nil {
		// Don't let garbage like "not-an-ip" through — a nil IP would cause
		// problems when we actually try to connect to this peer later.
		return errPeerDictMissingIP
	}
	// "peer id" is optional in BEP 3. Only set it if present and valid.
	if peerIdStr, ok := d["peer id"].(string); ok {
		p.ID = []byte(peerIdStr)
	}
	portVal, ok := d["port"].(int64)
	if !ok {
		return errPeerDictMissingPort
	}
	p.Port = int(portVal)
	return nil
}

func (p Peer) FromNodeAddr(na krpc.NodeAddr) Peer {
	p.IP = na.IP
	p.Port = na.Port
	return p
}
