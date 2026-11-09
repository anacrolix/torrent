package torrent

import (
	"net"
	"net/netip"
	"strings"
	"sync"
)

// Counts accepted connections that haven't finished the handshake phase, so that connections that
// never complete it can't be held without bound.
type pendingIncomingHandshakes struct {
	mu    sync.Mutex
	total int
	byIp  map[netip.Addr]int
}

func remoteNetipAddr(addr net.Addr) (ret netip.Addr, ok bool) {
	if addr == nil {
		return
	}
	addrPort, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return
	}
	return addrPort.Addr().Unmap().WithZone(""), true
}

// Reserves a handshake slot for an accepted connection. If ok, release must be called exactly once
// when the handshake phase ends.
func (me *pendingIncomingHandshakes) reserve(
	remote net.Addr,
	maxTotal, maxPerIp int,
) (release func(), ok bool) {
	ip, haveIp := remoteNetipAddr(remote)
	me.mu.Lock()
	defer me.mu.Unlock()
	if maxTotal > 0 && me.total >= maxTotal {
		return nil, false
	}
	if haveIp && maxPerIp > 0 && me.byIp[ip] >= maxPerIp {
		return nil, false
	}
	me.total++
	if haveIp {
		if me.byIp == nil {
			me.byIp = make(map[netip.Addr]int)
		}
		me.byIp[ip]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			me.mu.Lock()
			defer me.mu.Unlock()
			me.total--
			if haveIp {
				me.byIp[ip]--
				if me.byIp[ip] <= 0 {
					delete(me.byIp, ip)
				}
			}
		})
	}, true
}

func (cl *Client) reservePendingIncomingHandshake(remote net.Addr) (release func(), ok bool) {
	return cl.pendingIncomingHandshakes.reserve(
		remote,
		cl.config.MaxPendingIncomingHandshakes,
		cl.config.MaxPendingIncomingHandshakesPerIp,
	)
}

// Reports whether an accepted connection's source address has been shown to be genuine. A TCP
// accept implies a completed three-way handshake. uTP connections are accepted on receipt of a
// single datagram whose source can be forged, so require that the remote has sent us data first.
// This stops forged connection attempts from counting against another host in the accept limiter.
func acceptedConnSourceVerified(c *PeerConn) bool {
	if strings.HasPrefix(c.Network, "tcp") {
		return true
	}
	return c._stats.BytesRead.Int64() > 0
}
