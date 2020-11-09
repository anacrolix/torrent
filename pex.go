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

// facilitates efficient de-duplication while generating PEX messages
type pexMsgFactory struct {
	added   map[addrKey]pexEvent
	dropped map[addrKey]pexEvent
}

func (me *pexMsgFactory) DeltaLen() int {
	return int(max(
		int64(len(me.added)),
		int64(len(me.dropped))))
}

type addrKey string

// Returns the key to use to identify a given addr in the factory.
func (me *pexMsgFactory) addrKey(addr net.Addr) addrKey {
	return addrKey(addr.String())
}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) add(e pexEvent) {
	key := me.addrKey(e.addr)
	if _, ok := me.dropped[key]; ok {
		delete(me.dropped, key)
		return
	}
	if me.added == nil {
		me.added = make(map[addrKey]pexEvent, pexMaxDelta)
	}
	me.added[key] = e
}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) drop(e pexEvent) {
	key := me.addrKey(e.addr)
	if _, ok := me.added[key]; ok {
		delete(me.added, key)
		return
	}
	if me.dropped == nil {
		me.dropped = make(map[addrKey]pexEvent, pexMaxDelta)
	}
	me.dropped[key] = e
}

func (me *pexMsgFactory) addEvent(event pexEvent) {
	switch event.t {
	case pexAdd:
		me.add(event)
	case pexDrop:
		me.drop(event)
	default:
		panic(event.t)
	}
}

func (me *pexMsgFactory) PexMsg() (ret pp.PexMsg) {
	for key, added := range me.added {
		addr, ok := nodeAddr(added.addr)
		if !ok {
			continue
		}
		switch len(addr.IP) {
		case net.IPv4len:
			ret.Added = append(ret.Added, addr)
			ret.AddedFlags = append(ret.AddedFlags, added.f)
		case net.IPv6len:
			ret.Added6 = append(ret.Added6, addr)
			ret.Added6Flags = append(ret.Added6Flags, added.f)
		default:
			panic(key)
		}
	}
	for key, dropped := range me.dropped {
		addr, ok := nodeAddr(dropped.addr)
		if !ok {
			continue
		}
		switch len(addr.IP) {
		case net.IPv4len:
			ret.Dropped = append(ret.Dropped, addr)
		case net.IPv6len:
			ret.Dropped6 = append(ret.Dropped6, addr)
		default:
			panic(key)
		}
	}
	return
}

// Convert an arbitrary torrent peer Addr into one that can be represented by the compact addr
// format.
func nodeAddr(addr net.Addr) (_ krpc.NodeAddr, ok bool) {
	ipport, ok := tryIpPortFromNetAddr(addr)
	if !ok {
		return
	}
	return krpc.NodeAddr{IP: shortestIP(ipport.IP), Port: ipport.Port}, true
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
	c.pex.Listed = true
}

func (s *pexState) Drop(c *PeerConn) {
	if !c.pex.Listed {
		// skip connections which were not previously Added
		return
	}
	e := c.pexEvent(pexDrop)
	s.nc--
	if s.nc < pexTargAdded && len(s.hold) < pexMaxHold {
		s.hold = append(s.hold, e)
	} else {
		s.ev = append(s.ev, e)
	}
}

// Generate a PEX message based on the event feed. Also returns an index to pass to the subsequent
// calls, producing incremental deltas.
func (s *pexState) Genmsg(start int) (pp.PexMsg, int) {
	var factory pexMsgFactory
	n := start
	for _, e := range s.ev[start:] {
		if start > 0 && factory.DeltaLen() >= pexMaxDelta {
			break
		}
		factory.addEvent(e)
		n++
	}
	return factory.PexMsg(), n
}
