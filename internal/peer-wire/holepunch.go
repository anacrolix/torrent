package peerWire

import (
	"net/netip"
)

// BEP 55 ut_holepunch. We're the initiator when we send Rendezvous to the peer as a relay. The
// relay answers with Connect, or Error if it can't act on it.

// Most rendezvous awaiting a reply through one relay. The BEP says the relay SHOULD reply, so
// this bounds what a relay that doesn't can make us hold.
const MaxSentRendezvous = 64

// SentRendezvous records that we're sending a Rendezvous for target through the peer. It returns
// false if there are already MaxSentRendezvous awaiting a reply, in which case don't send it. A
// repeat for the same target is allowed.
func (s *State) SentRendezvous(target netip.AddrPort) (ok bool) {
	s.event()
	if _, ok := s.sentRendezvous[target]; ok {
		return true
	}
	if len(s.sentRendezvous) >= MaxSentRendezvous {
		return false
	}
	if s.sentRendezvous == nil {
		s.sentRendezvous = make(map[netip.AddrPort]struct{})
	}
	s.sentRendezvous[target] = struct{}{}
	return true
}

// RendezvousSent returns whether a Rendezvous for target through the peer awaits a reply.
func (s *State) RendezvousSent(target netip.AddrPort) bool {
	_, ok := s.sentRendezvous[target]
	return ok
}

// RecvHolepunchConnect handles a Connect for addr. initiated is true if it answers our
// Rendezvous. Otherwise we're the target of someone else's, which needs no prior message.
func (s *State) RecvHolepunchConnect(addr netip.AddrPort) (initiated bool) {
	s.event()
	return s.takeRendezvous(addr)
}

// RecvHolepunchError handles an Error for addr. solicited is false if we have no Rendezvous for
// addr through the peer, since only a relay answering one sends Error.
func (s *State) RecvHolepunchError(addr netip.AddrPort) (solicited bool) {
	s.event()
	return s.takeRendezvous(addr)
}

func (s *State) takeRendezvous(addr netip.AddrPort) bool {
	if _, ok := s.sentRendezvous[addr]; !ok {
		return false
	}
	delete(s.sentRendezvous, addr)
	return true
}
