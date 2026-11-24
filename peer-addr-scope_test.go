package torrent

import (
	"fmt"
	"net/netip"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

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
