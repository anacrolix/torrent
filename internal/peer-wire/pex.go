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

// What the peer has told us over ut_pex (BEP 11).
type pexState struct {
	// Running record of live connections the peer purports to have, with their flags.
	liveConns map[netip.AddrPort]pp.PexPeerFlags
	lastRecv  time.Time
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
