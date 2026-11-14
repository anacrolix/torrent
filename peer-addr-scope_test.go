package torrent

import (
	"fmt"
	"net"
	"net/netip"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
)

func TestPeerMayDirectDialTo(t *testing.T) {
	public := g.Some(netip.MustParseAddr("8.8.8.8"))
	lan := g.Some(netip.MustParseAddr("192.168.1.2"))
	for _, tc := range []struct {
		source g.Option[netip.Addr]
		target string
		ok     bool
	}{
		{public, "1.1.1.1", true},
		{public, "2001:4860::1", true},
		{public, "10.0.0.1", false},
		{public, "127.0.0.1", false},
		{public, "::ffff:127.0.0.1", false},
		{public, "::1", false},
		{public, "169.254.169.254", false},
		{public, "fe80::1", false},
		{public, "fd00::1", false},
		{public, "0.0.0.0", false},
		{public, "224.0.0.1", false},
		{g.None[netip.Addr](), "10.0.0.1", false},
		{g.None[netip.Addr](), "1.1.1.1", true},
		{lan, "10.0.0.1", true},
		{g.Some(netip.MustParseAddr("127.0.0.1")), "127.0.0.1", true},
	} {
		qt.Check(t,
			qt.Equals(peerMayDirectDialTo(tc.source, netip.MustParseAddr(tc.target)), tc.ok),
			qt.Commentf("%v -> %v", tc.source, tc.target))
	}
}

func TestPexFilterAdded(t *testing.T) {
	s := pexConnState{remoteIp: g.Some(netip.MustParseAddr("8.8.8.8"))}
	nas := []krpc.NodeAddr{
		{IP: net.ParseIP("10.0.0.1").To4(), Port: 1},
		{IP: net.ParseIP("1.1.1.1").To4(), Port: 2},
		{IP: net.ParseIP("127.0.0.1").To4(), Port: 3},
		{IP: net.ParseIP("1.0.0.1").To4(), Port: 4},
	}
	fs := []pp.PexPeerFlags{1, 2, 3, 4}
	gotNas, gotFs := s.filterAdded(nas, fs)
	qt.Check(t, qt.DeepEquals(gotNas, []krpc.NodeAddr{nas[1], nas[3]}))
	qt.Check(t, qt.DeepEquals(gotFs, []pp.PexPeerFlags{2, 4}))

	nas = nil
	for i := range 3 * maxPexAddedPerMessage {
		nas = append(nas, krpc.NodeAddr{IP: net.IPv4(1, 0, byte(i>>8), byte(i)).To4(), Port: 1})
	}
	gotNas, _ = s.filterAdded(nas, nil)
	qt.Check(t, qt.HasLen(gotNas, maxPexAddedPerMessage))
}

func TestPexRemoteLiveConnsBounded(t *testing.T) {
	var s pexConnState
	for i := range 2 * maxPexRemoteLiveConns / maxPexAddedPerMessage {
		var rx pp.PexMsg
		for j := range maxPexAddedPerMessage {
			rx.Added = append(rx.Added, krpc.NodeAddr{IP: net.IPv4(1, byte(i), byte(j>>8), byte(j)).To4(), Port: 1})
		}
		s.updateRemoteLiveConns(rx)
	}
	qt.Check(t, qt.HasLen(s.remoteLiveConns, maxPexRemoteLiveConns))
}

// A connection with no known local address must not have us dial local addresses.
func TestHolepunchConnectAddressScope(t *testing.T) {
	tt, c := newNetworkBoundsTestConn(t, TestingConfig(t), true)
	for i, addr := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.0.2.1"} {
		err := tt.handleReceivedUtHolepunchMsg(utHolepunch.Msg{
			MsgType:  utHolepunch.Connect,
			AddrPort: netip.AddrPortFrom(netip.MustParseAddr(addr), uint16(i+1)),
		}, c)
		qt.Assert(t, qt.IsNil(err))
	}
	qt.Check(t, qt.HasLen(tt.halfOpen, 1), qt.Commentf("%v", fmt.Sprint(tt.halfOpen)))
}
