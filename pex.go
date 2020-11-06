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

// Combines the node addr, as required for pp.PexMsg.
type pexMsgAdded struct {
	krpc.NodeAddr
	pp.PexPeerFlags
}

// Makes generating a PexMsg more efficient.
type pexMsgFactory struct {
	added   map[string]pexMsgAdded
	dropped map[string]krpc.NodeAddr
}

func (me *pexMsgFactory) DeltaLen() int {
	return int(max(
		int64(len(me.added)),
		int64(len(me.dropped))))
}

// Returns the key to use to identify a given addr in the factory. Panics if we can't support the
// addr later in generating a PexMsg (since adding an unusable addr will cause DeltaLen to be out.)
func (me *pexMsgFactory) addrKey(addr krpc.NodeAddr) string {
	if addr.IP.To4() != nil {
		addr.IP = addr.IP.To4()
	}
	keyBytes, err := addr.MarshalBinary()
	if err != nil {
		panic(err)
	}
	switch len(keyBytes) {
	case compactIpv4NodeAddrElemSize:
	case compactIpv6NodeAddrElemSize:
	default:
		panic(len(keyBytes))
	}
	return string(keyBytes)
}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) Add(addr krpc.NodeAddr, flags pp.PexPeerFlags) {
	key := me.addrKey(addr)
	if _, ok := me.dropped[key]; ok {
		delete(me.dropped, key)
		return
	}
	if me.added == nil {
		me.added = make(map[string]pexMsgAdded, pexMaxDelta)
	}
	me.added[key] = pexMsgAdded{addr, flags}

}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) Drop(addr krpc.NodeAddr) {
	key := me.addrKey(addr)
	if _, ok := me.added[key]; ok {
		delete(me.added, key)
		return
	}
	if me.dropped == nil {
		me.dropped = make(map[string]krpc.NodeAddr, pexMaxDelta)
	}
	me.dropped[key] = addr
}

func (me *pexMsgFactory) addEvent(event pexEvent) {
	addr, ok := nodeAddr(event.addr)
	if !ok {
		return
	}
	switch event.t {
	case pexAdd:
		me.Add(addr, event.f)
	case pexDrop:
		me.Drop(addr)
	default:
		panic(event.t)
	}
}

var compactIpv4NodeAddrElemSize = krpc.CompactIPv4NodeAddrs{}.ElemSize()
var compactIpv6NodeAddrElemSize = krpc.CompactIPv6NodeAddrs{}.ElemSize()

func (me *pexMsgFactory) PexMsg() (ret pp.PexMsg) {
	for key, added := range me.added {
		switch len(key) {
		case compactIpv4NodeAddrElemSize:
			ret.Added = append(ret.Added, added.NodeAddr)
			ret.AddedFlags = append(ret.AddedFlags, added.PexPeerFlags)
		case compactIpv6NodeAddrElemSize:
			ret.Added6 = append(ret.Added6, added.NodeAddr)
			ret.Added6Flags = append(ret.Added6Flags, added.PexPeerFlags)
		default:
			panic(key)
		}
	}
	for key, addr := range me.dropped {
		switch len(key) {
		case compactIpv4NodeAddrElemSize:
			ret.Dropped = append(ret.Dropped, addr)
		case compactIpv6NodeAddrElemSize:
			ret.Dropped6 = append(ret.Dropped6, addr)
		default:
			panic(key)
		}
	}
	return
}

func mustNodeAddr(addr net.Addr) krpc.NodeAddr {
	ret, ok := nodeAddr(addr)
	if !ok {
		panic(addr)
	}
	return ret
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
