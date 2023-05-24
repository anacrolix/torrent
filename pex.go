package torrent

import (
	"net"
	"net/netip"
	"sync"
	"time"

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
	addr netip.AddrPort
	f    pp.PexPeerFlags
	next *pexEvent // event feed list
}

// facilitates efficient de-duplication while generating PEX messages
type pexMsgFactory struct {
	msg     pp.PexMsg
	added   map[netip.AddrPort]struct{}
	dropped map[netip.AddrPort]struct{}
}

func (me *pexMsgFactory) DeltaLen() int {
	return int(max(
		int64(len(me.added)),
		int64(len(me.dropped))))
}

type addrKey = netip.AddrPort

// Returns the key to use to identify a given addr in the factory.
func (me *pexMsgFactory) addrKey(addr netip.AddrPort) addrKey {
	return addr
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
	addr := krpcNodeAddrFromAddrPort(e.addr)
	m := &me.msg
	switch {
	case addr.IP.To4() != nil:
		if _, ok := me.dropped[key]; ok {
			if i := m.Dropped.Index(addr); i >= 0 {
				m.Dropped = append(m.Dropped[:i], m.Dropped[i+1:]...)
			}
			delete(me.dropped, key)
			return
		}
		m.Added = append(m.Added, addr)
		m.AddedFlags = append(m.AddedFlags, e.f)
	case len(addr.IP) == net.IPv6len:
		if _, ok := me.dropped[key]; ok {
			if i := m.Dropped6.Index(addr); i >= 0 {
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
	addr := krpcNodeAddrFromAddrPort(e.addr)
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
			if i := m.Added.Index(addr); i >= 0 {
				m.Added = append(m.Added[:i], m.Added[i+1:]...)
				m.AddedFlags = append(m.AddedFlags[:i], m.AddedFlags[i+1:]...)
			}
			delete(me.added, key)
			return
		}
		m.Dropped = append(m.Dropped, addr)
	case len(addr.IP) == net.IPv6len:
		if _, ok := me.added[key]; ok {
			if i := m.Added6.Index(addr); i >= 0 {
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

func (me *pexMsgFactory) append(event pexEvent) {
	switch event.t {
	case pexAdd:
		me.add(event)
	case pexDrop:
		me.drop(event)
	default:
		panic(event.t)
	}
}

func (me *pexMsgFactory) PexMsg() *pp.PexMsg {
	return &me.msg
}

// Per-torrent PEX state
type pexState struct {
	sync.RWMutex
	tail *pexEvent  // event feed list
	hold []pexEvent // delayed drops
	// Torrent-wide cooldown deadline on inbound. This exists to prevent PEX from drowning out other
	// peer address sources, until that is fixed.
	rest time.Time
	nc   int           // net number of alive conns
	msg0 pexMsgFactory // initial message
}

// Reset wipes the state clean, releasing resources. Called from Torrent.Close().
func (s *pexState) Reset() {
	s.Lock()
	defer s.Unlock()
	s.tail = nil
	s.hold = nil
	s.nc = 0
	s.rest = time.Time{}
	s.msg0 = pexMsgFactory{}
}

func (s *pexState) append(e *pexEvent) {
	if s.tail != nil {
		s.tail.next = e
	}
	s.tail = e
	s.msg0.append(*e)
}

func (s *pexState) Add(c *PeerConn) {
	e, err := c.pexEvent(pexAdd)
	if err != nil {
		return
	}
	s.Lock()
	defer s.Unlock()
	s.nc++
	if s.nc >= pexTargAdded {
		for _, e := range s.hold {
			ne := e
			s.append(&ne)
		}
		s.hold = s.hold[:0]
	}
	c.pex.Listed = true
	s.append(&e)
}

func (s *pexState) Drop(c *PeerConn) {
	if !c.pex.Listed {
		// skip connections which were not previously Added
		return
	}
	e, err := c.pexEvent(pexDrop)
	if err != nil {
		return
	}
	s.Lock()
	defer s.Unlock()
	s.nc--
	if s.nc < pexTargAdded && len(s.hold) < pexMaxHold {
		s.hold = append(s.hold, e)
	} else {
		s.append(&e)
	}
}

// Generate a PEX message based on the event feed.
// Also returns a pointer to pass to the subsequent calls
// to produce incremental deltas.
func (s *pexState) Genmsg(start *pexEvent) (pp.PexMsg, *pexEvent) {
	s.RLock()
	defer s.RUnlock()
	if start == nil {
		return *s.msg0.PexMsg(), s.tail
	}
	var msg pexMsgFactory
	last := start
	for e := start.next; e != nil; e = e.next {
		if msg.DeltaLen() >= pexMaxDelta {
			break
		}
		msg.append(*e)
		last = e
	}
	return *msg.PexMsg(), last
}

// The same as Genmsg but just counts up the distinct events that haven't been sent.
func (s *pexState) numPending(start *pexEvent) (num int) {
	s.RLock()
	defer s.RUnlock()
	if start == nil {
		return s.msg0.PexMsg().Len()
	}
	for e := start.next; e != nil; e = e.next {
		num++
	}
	return
}
