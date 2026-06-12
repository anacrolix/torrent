package torrent

import (
	"hash/maphash"
	"net"
	"net/netip"
	"testing"

	qt "github.com/go-quicktest/qt"
	"github.com/google/btree"
)

func TestPrioritizedPeers(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(p PeerInfo) peerPriority {
			return bep40PriorityIgnoreError(p.addr(), IpPort{IP: net.ParseIP("0.0.0.0")})
		},
	}
	_, ok := pp.DeleteMin()
	qt.Check(t, qt.PanicMatches(func() { pp.PopMax() }, ".*"))
	qt.Check(t, qt.IsFalse(ok))
	ps := []PeerInfo{
		{Addr: ipPortAddr{IP: net.ParseIP("1.2.3.4")}},
		{Addr: ipPortAddr{IP: net.ParseIP("1::2")}},
		{Addr: ipPortAddr{IP: net.ParseIP("")}},
		{Addr: ipPortAddr{IP: net.ParseIP("")}, Trusted: true},
	}
	for i, p := range ps {
		t.Logf("peer %d priority: %08x trusted: %t\n", i, pp.getPrio(p), p.Trusted)
		qt.Check(t, qt.IsFalse(pp.Add(p)))
		qt.Check(t, qt.IsTrue(pp.Add(p)))
		qt.Check(t, qt.Equals(pp.Len(), i+1))
	}
	pop := func(expected *PeerInfo) {
		if expected == nil {
			qt.Check(t, qt.PanicMatches(func() { pp.PopMax() }, ".*"))
		} else {
			qt.Check(t, qt.DeepEquals(pp.PopMax(), *expected))
		}
	}
	min := func(expected *PeerInfo) {
		i, ok := pp.DeleteMin()
		if expected == nil {
			qt.Check(t, qt.IsFalse(ok))
		} else {
			qt.Check(t, qt.IsTrue(ok))
			qt.Check(t, qt.DeepEquals(i.p, *expected))
		}
	}
	pop(&ps[3])
	pop(&ps[1])
	min(&ps[2])
	pop(&ps[0])
	min(nil)
	pop(nil)
}

// TestPrioritizedPeersAddrHash verifies that the hashing path preserves the exact
// address-string hash used to order peers in the tree.
func TestPrioritizedPeersAddrHash(t *testing.T) {
	testCases := []PeerRemoteAddr{
		StringAddr("192.0.2.1:1234"),
		ipPortAddr{IP: net.ParseIP("192.0.2.1"), Port: 1234},
		ipPortAddr{IP: net.ParseIP("2001:db8::1"), Port: 5678},
		netip.MustParseAddrPort("198.51.100.7:4321"),
		netip.MustParseAddrPort("[2001:db8::2]:8765"),
	}
	for _, addr := range testCases {
		t.Run(addr.String(), func(t *testing.T) {
			item := newPrioritizedPeersItem(7, PeerInfo{Addr: addr})
			var expected maphash.Hash
			expected.SetSeed(hashSeed)
			expected.WriteString(addr.String())
			qt.Check(t, qt.Equals(item.addrHash, int64(expected.Sum64())))
		})
	}
}
