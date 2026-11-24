package torrent

import (
	"net"
	"net/netip"
	"testing"

	qt "github.com/go-quicktest/qt"
)

type nonIpAddr struct{}

func (nonIpAddr) Network() string { return "test" }
func (nonIpAddr) String() string  { return "not an ip" }

// Peers with IPv6 zoned addresses can be banned by IP.
func TestBanPeerWithZonedIp(t *testing.T) {
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	pc := cl.newConnection(nil, newConnectionOpts{
		network:    "tcp",
		remoteAddr: &net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 1234, Zone: "eth0"},
	})
	pc.setTorrent(tor)
	cl.lock()
	defer cl.unlock()
	pc.providedBadData()
	_, ok := cl.badPeerIPs[netip.MustParseAddr("fe80::1")]
	qt.Assert(t, qt.IsTrue(ok))
}

// Peers whose address has no IP are dropped instead of banned.
func TestBanPeerWithoutIp(t *testing.T) {
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	pc := cl.newConnection(nil, newConnectionOpts{
		network:    "test",
		remoteAddr: nonIpAddr{},
	})
	pc.setTorrent(tor)
	cl.lock()
	defer cl.unlock()
	pc.providedBadData()
	qt.Assert(t, qt.HasLen(cl.badPeerIPs, 0))
	qt.Assert(t, qt.IsTrue(pc.closed.IsSet()))
	cl.banPeerIP(nil)
}
