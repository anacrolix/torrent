package torrent

import (
	"net"
	"sync"
	"time"

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
	addr PeerRemoteAddr
	f    pp.PexPeerFlags
}

// facilitates efficient de-duplication while generating PEX messages
type pexMsgFactory struct {
	msg     pp.PexMsg
	added   map[addrKey]struct{}
	dropped map[addrKey]struct{}
}

func (me *pexMsgFactory) DeltaLen() int {
	if len(me.added) > len(me.dropped) {
		return len(me.added)
	}
	return len(me.dropped)
}

type addrKey string

// Returns the key to use to identify a given addr in the factory.
func (me *pexMsgFactory) addrKey(addr PeerRemoteAddr) addrKey {
	return addrKey(addr.String())
}

func addrEqual(a, b *krpc.NodeAddr) bool {
	return a.IP.Equal(b.IP) && a.Port == b.Port
}

func addrIndex(v []krpc.NodeAddr, a *krpc.NodeAddr) int {
	for i := range v {
		if addrEqual(&v[i], a) {
			return i
		}
	}
	return -1
}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) add(e pexEvent) {
	key := me.addrKey(e.addr)
	if _, ok := me.added[key]; ok {
		return
	}
	if me.added == nil {
		me.added = make(map[addrKey]struct{}, pexMaxDelta)
	}
	addr, ok := nodeAddr(e.addr)
	if !ok {
		return
	}
	m := &me.msg
	switch {
	case addr.IP.To4() != nil:
		if _, ok := me.dropped[key]; ok {
			if i := addrIndex(m.Dropped.NodeAddrs(), &addr); i >= 0 {
				m.Dropped = append(m.Dropped[:i], m.Dropped[i+1:]...)
			}
			delete(me.dropped, key)
			return
		}
		m.Added = append(m.Added, addr)
		m.AddedFlags = append(m.AddedFlags, e.f)
	case len(addr.IP) == net.IPv6len:
		if _, ok := me.dropped[key]; ok {
			if i := addrIndex(m.Dropped6.NodeAddrs(), &addr); i >= 0 {
				m.Dropped6 = append(m.Dropped6[:i], m.Dropped6[i+1:]...)
			}
			delete(me.dropped, key)
			return
		}
		m.Added6 = append(m.Added6, addr)
		m.Added6Flags = append(m.Added6Flags, e.f)
	default:
		panic(addr)
	}
	me.added[key] = struct{}{}
}

// Returns whether the entry was added (we can check if we're cancelling out another entry and so
// won't hit the limit consuming this event).
func (me *pexMsgFactory) drop(e pexEvent) {
	addr, ok := nodeAddr(e.addr)
	if !ok {
		return
	}
	key := me.addrKey(e.addr)
	if me.dropped == nil {
		me.dropped = make(map[addrKey]struct{}, pexMaxDelta)
	}
	if _, ok := me.dropped[key]; ok {
		return
	}
	m := &me.msg
	switch {
	case addr.IP.To4() != nil:
		if _, ok := me.added[key]; ok {
			if i := addrIndex(m.Added.NodeAddrs(), &addr); i >= 0 {
				m.Added = append(m.Added[:i], m.Added[i+1:]...)
				m.AddedFlags = append(m.AddedFlags[:i], m.AddedFlags[i+1:]...)
			}
			delete(me.added, key)
			return
		}
		m.Dropped = append(m.Dropped, addr)
	case len(addr.IP) == net.IPv6len:
		if _, ok := me.added[key]; ok {
			if i := addrIndex(m.Added6.NodeAddrs(), &addr); i >= 0 {
				m.Added6 = append(m.Added6[:i], m.Added6[i+1:]...)
				m.Added6Flags = append(m.Added6Flags[:i], m.Added6Flags[i+1:]...)
			}
			delete(me.added, key)
			return
		}
		m.Dropped6 = append(m.Dropped6, addr)
	}
	me.dropped[key] = struct{}{}
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

func (me *pexMsgFactory) PexMsg() pp.PexMsg {
	return me.msg
}

// Convert an arbitrary torrent peer Addr into one that can be represented by the compact addr
// format.
func nodeAddr(addr PeerRemoteAddr) (krpc.NodeAddr, bool) {
	ipport, _ := tryIpPortFromNetAddr(addr)
	ok := ipport.IP != nil
	return krpc.NodeAddr{IP: shortestIP(ipport.IP), Port: ipport.Port}, ok
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
	ev        []pexEvent    // event feed, append-only
	hold      []pexEvent    // delayed drops
	rest      time.Time     // cooldown deadline on inbound
	nc        int           // net number of alive conns
	initCache pexMsgFactory // last generated initial message
	initSeq   int           // number of events which went into initCache
	initLock  sync.RWMutex  // serialise access to initCache and initSeq
}

// Reset wipes the state clean, releasing resources. Called from Torrent.Close().
func (s *pexState) Reset() {
	s.ev = nil
	s.hold = nil
	s.nc = 0
	s.rest = time.Time{}
	s.initLock.Lock()
	s.initCache = pexMsgFactory{}
	s.initSeq = 0
	s.initLock.Unlock()
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
	if start == 0 {
		return s.genmsg0()
	}

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

func (s *pexState) genmsg0() (pp.PexMsg, int) {
	s.initLock.Lock()
	for _, e := range s.ev[s.initSeq:] {
		s.initCache.addEvent(e)
		s.initSeq++
	}
	s.initLock.Unlock()
	s.initLock.RLock()
	n := s.initSeq
	msg := s.initCache.PexMsg()
	s.initLock.RUnlock()
	return msg, n
}
