package peerWire

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func allowAll(netip.Addr) bool { return true }

func TestRecvPexRateLimited(t *testing.T) {
	var s State
	now := time.Unix(1e9, 0)
	_, err := s.RecvPex(&pp.PexMsg{}, now, allowAll)
	qt.Assert(t, qt.IsNil(err))
	_, err = s.RecvPex(&pp.PexMsg{}, now.Add(PexMinRecvInterval-1), allowAll)
	qt.Check(t, qt.IsNotNil(err))
	// A refused message doesn't restart the interval.
	_, err = s.RecvPex(&pp.PexMsg{}, now.Add(PexMinRecvInterval), allowAll)
	qt.Check(t, qt.IsNil(err))
}

func TestRecvPexFiltersAdded(t *testing.T) {
	var s State
	rx := pp.PexMsg{
		Added: []krpc.NodeAddr{
			{IP: net.ParseIP("10.0.0.1").To4(), Port: 1},
			{IP: net.ParseIP("1.1.1.1").To4(), Port: 2},
			{IP: net.ParseIP("127.0.0.1").To4(), Port: 3},
			{IP: net.ParseIP("1.0.0.1").To4(), Port: 4},
		},
		AddedFlags: []pp.PexPeerFlags{1, 2, 3, 4},
	}
	orig := rx.Added
	res, err := s.RecvPex(&rx, time.Unix(1e9, 0), func(addr netip.Addr) bool {
		return addr.IsGlobalUnicast() && !addr.IsPrivate()
	})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(rx.Added, []krpc.NodeAddr{orig[1], orig[3]}))
	qt.Check(t, qt.DeepEquals(rx.AddedFlags, []pp.PexPeerFlags{2, 4}))
	qt.Check(t, qt.Equals(res, PexRecv{DroppedForScope: 2}))
	flags, ok := s.PeerPexLiveConn(netip.MustParseAddrPort("1.0.0.1:4"))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(flags, 4))
	_, ok = s.PeerPexLiveConn(netip.MustParseAddrPort("10.0.0.1:1"))
	qt.Check(t, qt.IsFalse(ok))

	rx = pp.PexMsg{}
	for i := range 3 * MaxPexAddedPerMessage {
		rx.Added = append(rx.Added, krpc.NodeAddr{IP: net.IPv4(1, 0, byte(i>>8), byte(i)).To4(), Port: 1})
	}
	res, err = s.RecvPex(&rx, time.Unix(2e9, 0), allowAll)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(rx.Added, MaxPexAddedPerMessage))
	qt.Check(t, qt.Equals(res, PexRecv{DroppedPastLimit: 2 * MaxPexAddedPerMessage}))
}

func TestRecvPexDropped(t *testing.T) {
	var s State
	na := krpc.NodeAddr{IP: net.ParseIP("2001:db8::1"), Port: 6881}
	_, err := s.RecvPex(&pp.PexMsg{Added6: []krpc.NodeAddr{na}}, time.Unix(1e9, 0), allowAll)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(s.NumPeerPexLiveConns(), 1))
	flags, ok := s.PeerPexLiveConn(netip.MustParseAddrPort("[2001:db8::1]:6881"))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(flags, 0))
	_, err = s.RecvPex(&pp.PexMsg{Dropped6: []krpc.NodeAddr{na}}, time.Unix(2e9, 0), allowAll)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(s.NumPeerPexLiveConns(), 0))
}

func TestPexLiveConnsBounded(t *testing.T) {
	var s State
	now := time.Unix(1e9, 0)
	for i := range 2 * MaxPexRemoteLiveConns / MaxPexAddedPerMessage {
		var rx pp.PexMsg
		for j := range MaxPexAddedPerMessage {
			rx.Added = append(rx.Added, krpc.NodeAddr{IP: net.IPv4(1, byte(i), byte(j>>8), byte(j)).To4(), Port: 1})
		}
		_, err := s.RecvPex(&rx, now, allowAll)
		qt.Assert(t, qt.IsNil(err))
		now = now.Add(PexMinRecvInterval)
	}
	qt.Check(t, qt.Equals(s.NumPeerPexLiveConns(), MaxPexRemoteLiveConns))
}

func pexNodeAddr(s string) krpc.NodeAddr {
	ap := netip.MustParseAddrPort(s)
	return krpc.NodeAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())}
}

func TestSendPex(t *testing.T) {
	var s State
	now := time.Unix(1e9, 0)
	qt.Check(t, qt.Equals(s.PexSendWait(now), time.Duration(0)))
	a, b, c := pexNodeAddr("1.0.0.1:1"), pexNodeAddr("1.0.0.2:1"), pexNodeAddr("[2001:db8::1]:1")
	msg := pp.PexMsg{
		Added:      []krpc.NodeAddr{a, b, a},
		AddedFlags: []pp.PexPeerFlags{1, 2, 3},
		Added6:     []krpc.NodeAddr{c},
		// Never added, so not dropped.
		Dropped: []krpc.NodeAddr{pexNodeAddr("1.0.0.9:1")},
	}
	shared := msg.Added
	qt.Assert(t, qt.IsTrue(s.SendPex(&msg, now)))
	qt.Check(t, qt.DeepEquals([]krpc.NodeAddr(msg.Added), []krpc.NodeAddr{a, b}))
	qt.Check(t, qt.DeepEquals(msg.AddedFlags, []pp.PexPeerFlags{1, 2}))
	qt.Check(t, qt.HasLen(msg.Dropped, 0))
	qt.Check(t, qt.DeepEquals([]krpc.NodeAddr(shared), []krpc.NodeAddr{a, b, a}), qt.Commentf("caller's slices unchanged"))
	qt.Check(t, qt.IsTrue(s.PexAdvertised(netip.MustParseAddrPort("[2001:db8::1]:1"))))

	// Too soon.
	later := now.Add(PexSendInterval / 2)
	qt.Check(t, qt.Equals(s.PexSendWait(later), PexSendInterval/2))
	qt.Check(t, qt.IsFalse(s.SendPex(&pp.PexMsg{Dropped: []krpc.NodeAddr{a}}, later)))
	qt.Check(t, qt.IsTrue(s.PexAdvertised(netip.MustParseAddrPort("1.0.0.1:1"))))

	later = now.Add(PexSendInterval)
	// Nothing new: a is already advertised.
	qt.Check(t, qt.IsFalse(s.SendPex(&pp.PexMsg{Added: []krpc.NodeAddr{a}}, later)))
	msg = pp.PexMsg{Dropped: []krpc.NodeAddr{a}}
	qt.Assert(t, qt.IsTrue(s.SendPex(&msg, later)))
	qt.Check(t, qt.IsFalse(s.PexAdvertised(netip.MustParseAddrPort("1.0.0.1:1"))))
}
