package torrent

import (
	"net"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
	"github.com/google/btree"
)

func TestIsLocalIp(t *testing.T) {
	for _, tc := range []struct {
		ip    string
		local bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"192.168.1.10", true},
		{"169.254.0.1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"1.2.3.4", false},
		{"8.8.8.8", false},
		{"2001:db8::1", false},
	} {
		qt.Check(t, qt.Equals(isLocalIp(net.ParseIP(tc.ip)), tc.local), qt.Commentf("%v", tc.ip))
	}
	qt.Check(t, qt.IsFalse(isLocalIp(nil)))
}

func TestPrioritizedPeersLocalFirst(t *testing.T) {
	newPeers := func(prioritizeLocal bool) prioritizedPeers {
		return prioritizedPeers{
			om: btree.New(3),
			getPrio: func(p PeerInfo) peerPriority {
				// Rank remote peers above local ones by BEP 40 priority, so only locality can put
				// the local peer first.
				if p.isLocal() {
					return 0
				}
				return 1
			},
			prioritizeLocal: prioritizeLocal,
		}
	}
	remote := PeerInfo{Addr: ipPortAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}}
	local := PeerInfo{Addr: ipPortAddr{IP: net.ParseIP("192.168.1.2"), Port: 1}}
	trusted := PeerInfo{Addr: ipPortAddr{IP: net.ParseIP("5.6.7.8"), Port: 1}, Trusted: true}

	pp := newPeers(false)
	pp.Add(local)
	pp.Add(remote)
	qt.Check(t, qt.DeepEquals(pp.PopMax(), remote))

	pp = newPeers(true)
	pp.Add(local)
	pp.Add(remote)
	pp.Add(trusted)
	qt.Check(t, qt.DeepEquals(pp.PopMax(), trusted))
	qt.Check(t, qt.DeepEquals(pp.PopMax(), local))
	qt.Check(t, qt.DeepEquals(pp.PopMax(), remote))
}

func TestWorseConnLocal(t *testing.T) {
	remote := &worseConnInput{Useful: true, LastHelpful: time.Now(), Pointer: 1}
	local := &worseConnInput{Useful: true, Local: true, Pointer: 2}
	// A local conn outranks a remote one that was more recently helpful.
	qt.Check(t, qt.IsTrue(remote.Less(local)))
	qt.Check(t, qt.IsFalse(local.Less(remote)))
	// But usefulness still comes first.
	local.Useful = false
	qt.Check(t, qt.IsTrue(local.Less(remote)))
}

func TestLocalPeerMayStealRequest(t *testing.T) {
	tor := &Torrent{cl: &Client{config: &ClientConfig{}}}
	local := &Peer{RemoteAddr: ipPortAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}}
	remote := &Peer{RemoteAddr: ipPortAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}}
	_, ok := tor.localPeerMayStealRequest(local, remote)
	qt.Check(t, qt.IsFalse(ok))

	tor.cl.config.PrioritizeLocalPeers = true
	steal, ok := tor.localPeerMayStealRequest(local, remote)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(steal))
	steal, ok = tor.localPeerMayStealRequest(remote, local)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsFalse(steal))
	// Locality doesn't decide between two peers of the same kind.
	_, ok = tor.localPeerMayStealRequest(remote, remote)
	qt.Check(t, qt.IsFalse(ok))
}
