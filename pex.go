package torrent

import (
	"net"

	"github.com/anacrolix/dht/v2/krpc"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type pexEventType int

const (
	pexAdd pexEventType = iota
	pexDrop
)

// internal, based on BEP11
const (
	pexTargAdded = 25 // put drops on hold when the number of alive connections is lower than this
	pexMaxHold   = 25 // length of the drop hold-back buffer
	pexMaxDelta  = 50 // upper bound on added+added6 and dropped+dropped6 in a single PEX message
)

// represents a single connection (t=pexAdd) or disconnection (t=pexDrop) event
type pexEvent struct {
	t    pexEventType
	addr net.Addr
	f    pp.PexPeerFlags
}

// records the event into the peer protocol PEX message
func (e *pexEvent) put(m *pp.PexMsg) {
	switch e.t {
	case pexAdd:
		m.Add(nodeAddr(e.addr), e.f)
	case pexDrop:
		m.Drop(nodeAddr(e.addr))
	}
}

func nodeAddr(addr net.Addr) krpc.NodeAddr {
	ipport, _ := tryIpPortFromNetAddr(addr)
	return krpc.NodeAddr{IP: shortestIP(ipport.IP), Port: ipport.Port}
}

// mainly for the krpc marshallers
func shortestIP(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip
}

// Per-torrent PEX state
type pexState struct {
	ev   []pexEvent // event feed, append-only
	hold []pexEvent // delayed drops
	nc   int        // net number of alive conns
}

func (s *pexState) Reset() {
	s.ev = nil
	s.hold = nil
	s.nc = 0
}

func (s *pexState) Add(c *PeerConn) {
	s.nc++
	if s.nc >= pexTargAdded {
		s.ev = append(s.ev, s.hold...)
		s.hold = s.hold[:0]
	}
	e := c.pexEvent(pexAdd)
	s.ev = append(s.ev, e)
}

func (s *pexState) Drop(c *PeerConn) {
	e := c.pexEvent(pexDrop)
	s.nc--
	if s.nc < pexTargAdded && len(s.hold) < pexMaxHold {
		s.hold = append(s.hold, e)
	} else {
		s.ev = append(s.ev, e)
	}
}

// Generate a PEX message based on the event feed.
// Also returns an index to pass to the subsequent calls, producing incremental deltas.
func (s *pexState) Genmsg(start int) (*pp.PexMsg, int) {
	m := new(pp.PexMsg)
	n := start
	for _, e := range s.ev[start:] {
		if start > 0 && m.DeltaLen() >= pexMaxDelta {
			break
		}
		e.put(m)
		n++
	}
	return m, n
}
