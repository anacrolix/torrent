package peerWire

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/anacrolix/dht/v2/krpc"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	// BEP 11: "Clients must batch updates to send no more than 1 PEX message per minute." We allow
	// some slack.
	PexMinRecvInterval = 45 * time.Second
	// Most added peers used from each address family of a received PEX message. BEP 11 asks
	// senders for no more than 50 per message.
	MaxPexAddedPerMessage = 100
	// Most live connections recorded as purported by the peer.
	MaxPexRemoteLiveConns = 1000
)

// What we and the peer have told each other over ut_pex (BEP 11).
type pexState struct {
	// Running record of live connections the peer purports to have, with their flags.
	liveConns map[netip.AddrPort]pp.PexPeerFlags
	lastRecv  time.Time
	// Peers we've told the peer about in added fields, and not since dropped.
	advertised map[netip.AddrPort]struct{}
	lastSent   time.Time
}

// PexRecv counts what RecvPex dropped from a message.
type PexRecv struct {
	// Added peers past MaxPexAddedPerMessage for their address family.
	DroppedPastLimit int
	// Added peers that mayDial refused.
	DroppedForScope int
}

// RecvPex applies a PEX message received at now. It's an error if the previous one was less than
// PexMinRecvInterval earlier, and the message is then ignored. Otherwise rx's added peers are cut
// to MaxPexAddedPerMessage per family and filtered by mayDial, in place, with flags kept aligned,
// and the peer's live connections are updated from what remains and from the dropped peers.
func (s *State) RecvPex(
	rx *pp.PexMsg,
	now time.Time,
	mayDial func(netip.Addr) bool,
) (res PexRecv, err error) {
	s.event()
	if since := now.Sub(s.pex.lastRecv); since < PexMinRecvInterval {
		err = fmt.Errorf("last received only %v ago", since)
		return
	}
	s.pex.lastRecv = now
	rx.Added, rx.AddedFlags = filterPexAdded(rx.Added, rx.AddedFlags, mayDial, &res)
	rx.Added6, rx.Added6Flags = filterPexAdded(rx.Added6, rx.Added6Flags, mayDial, &res)
	s.pex.update(rx)
	return
}

// PeerPexLiveConn returns whether the peer has told us over PEX that it's connected to addrPort,
// and the flags it gave, which are zero if it didn't give any.
func (s *State) PeerPexLiveConn(addrPort netip.AddrPort) (flags pp.PexPeerFlags, ok bool) {
	flags, ok = s.pex.liveConns[addrPort]
	return
}

// NumPeerPexLiveConns returns how many live connections the peer has told us of over PEX.
func (s *State) NumPeerPexLiveConns() int {
	return len(s.pex.liveConns)
}

func filterPexAdded(
	nas []krpc.NodeAddr,
	fs []pp.PexPeerFlags,
	mayDial func(netip.Addr) bool,
	res *PexRecv,
) (retNas []krpc.NodeAddr, retFs []pp.PexPeerFlags) {
	for i, na := range nas {
		if len(retNas) >= MaxPexAddedPerMessage {
			res.DroppedPastLimit += len(nas) - i
			break
		}
		addr, ok := netip.AddrFromSlice(na.IP)
		if !ok || !mayDial(addr) {
			res.DroppedForScope++
			continue
		}
		retNas = append(retNas, na)
		if i < len(fs) {
			retFs = append(retFs, fs[i])
		}
	}
	return
}

func pexAddrPort(na krpc.NodeAddr, v6 bool) (_ netip.AddrPort, ok bool) {
	var addr netip.Addr
	if v6 {
		ip := na.IP.To16()
		if ip == nil {
			return
		}
		addr = netip.AddrFrom16([16]byte(ip))
	} else {
		ip := na.IP.To4()
		if ip == nil {
			return
		}
		addr = netip.AddrFrom4([4]byte(ip))
	}
	return netip.AddrPortFrom(addr, uint16(na.Port)), true
}

func (s *pexState) update(rx *pp.PexMsg) {
	drop := func(nas []krpc.NodeAddr, v6 bool) {
		for _, na := range nas {
			if addrPort, ok := pexAddrPort(na, v6); ok {
				delete(s.liveConns, addrPort)
			}
		}
	}
	drop(rx.Dropped, false)
	drop(rx.Dropped6, true)
	add := func(nas []krpc.NodeAddr, fs []pp.PexPeerFlags, v6 bool) {
		for i, na := range nas {
			addrPort, ok := pexAddrPort(na, v6)
			if !ok {
				continue
			}
			if _, ok := s.liveConns[addrPort]; !ok && len(s.liveConns) >= MaxPexRemoteLiveConns {
				continue
			}
			if s.liveConns == nil {
				s.liveConns = make(map[netip.AddrPort]pp.PexPeerFlags)
			}
			var flags pp.PexPeerFlags
			if i < len(fs) {
				flags = fs[i]
			}
			s.liveConns[addrPort] = flags
		}
	}
	add(rx.Added, rx.AddedFlags, false)
	add(rx.Added6, rx.Added6Flags, true)
}

// Sending PEX. BEP 11: "Clients must batch updates to send no more than 1 PEX message per
// minute", "A peer must only be included in an added field once a connection is successfully
// established", "Once a peer has been added a client must ensure to send the corresponding dropped
// event", "An added contact must not be dropped in the same message", and "Added or removed
// contacts must not contain duplicates".

const (
	PexSendInterval = time.Minute
	// Most peers we track having advertised to one peer. Past this, further peers aren't added.
	MaxPexAdvertised = 10000
)

// PexSendWait returns how long until a PEX message may be sent to the peer at now.
func (s *State) PexSendWait(now time.Time) time.Duration {
	if s.pex.lastSent.IsZero() {
		return 0
	}
	return max(0, PexSendInterval-now.Sub(s.pex.lastSent))
}

// SendPex prepares msg to be sent to the peer at now, replacing its fields with filtered copies: added peers we've
// already advertised and dropped peers we haven't are removed, as are duplicates and peers both
// added and dropped. If anything is left, it's recorded as sent and send is true. It returns
// false without changing anything if PexSendWait isn't zero.
func (s *State) SendPex(msg *pp.PexMsg, now time.Time) (send bool) {
	s.event()
	if s.PexSendWait(now) != 0 {
		return false
	}
	var f pexSendFilter
	filterPexDropped(&f, &msg.Dropped, false, s)
	filterPexDropped(&f, &msg.Dropped6, true, s)
	filterPexSendAdded(&f, &msg.Added, &msg.AddedFlags, false, s)
	filterPexSendAdded(&f, &msg.Added6, &msg.Added6Flags, true, s)
	if msg.Len() == 0 {
		return false
	}
	for ap := range f.drop {
		delete(s.pex.advertised, ap)
	}
	for ap := range f.add {
		if s.pex.advertised == nil {
			s.pex.advertised = make(map[netip.AddrPort]struct{})
		}
		s.pex.advertised[ap] = struct{}{}
	}
	s.pex.lastSent = now
	return true
}

// PexAdvertised returns whether we've told the peer about addrPort and not dropped it since.
func (s *State) PexAdvertised(addrPort netip.AddrPort) bool {
	_, ok := s.pex.advertised[addrPort]
	return ok
}

type pexSendFilter struct {
	add, drop map[netip.AddrPort]struct{}
}

func filterPexDropped[T ~[]krpc.NodeAddr](f *pexSendFilter, nas *T, v6 bool, s *State) {
	// Don't filter in place: the slices may be shared with the caller's other messages.
	var kept T
	for _, na := range *nas {
		ap, ok := pexAddrPort(na, v6)
		if !ok || !s.PexAdvertised(ap) {
			continue
		}
		if _, dup := f.drop[ap]; dup {
			continue
		}
		if f.drop == nil {
			f.drop = make(map[netip.AddrPort]struct{})
		}
		f.drop[ap] = struct{}{}
		kept = append(kept, na)
	}
	*nas = kept
}

func filterPexSendAdded[T ~[]krpc.NodeAddr](
	f *pexSendFilter,
	nas *T,
	fs *[]pp.PexPeerFlags,
	v6 bool,
	s *State,
) {
	var keptNas T
	var keptFs []pp.PexPeerFlags
	for i, na := range *nas {
		ap, ok := pexAddrPort(na, v6)
		if !ok || s.PexAdvertised(ap) {
			continue
		}
		if _, dup := f.add[ap]; dup {
			continue
		}
		if _, dropped := f.drop[ap]; dropped {
			continue
		}
		if len(s.pex.advertised)+len(f.add) >= MaxPexAdvertised {
			break
		}
		if f.add == nil {
			f.add = make(map[netip.AddrPort]struct{})
		}
		f.add[ap] = struct{}{}
		keptNas = append(keptNas, na)
		if i < len(*fs) {
			keptFs = append(keptFs, (*fs)[i])
		}
	}
	*nas = keptNas
	*fs = keptFs
}
