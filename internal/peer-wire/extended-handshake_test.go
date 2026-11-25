package peerWire

import (
	"testing"

	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func TestRecvExtendedHandshake(t *testing.T) {
	h := ExtendedHandshake{PeerMaxRequests: 250}
	newNames := RecvExtendedHandshake(&h, &pp.ExtendedHandshakeMessage{
		M:          map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNamePex: 1},
		V:          "test 1.0",
		Port:       1234,
		Encryption: true,
	}, 2)
	qt.Check(t, qt.DeepEquals(newNames, []pp.ExtensionName{pp.ExtensionNamePex}))
	// reqq wasn't given, so the default stays.
	qt.Check(t, qt.Equals(h.PeerMaxRequests, 250))
	qt.Check(t, qt.Equals(h.PeerClientName.Load(), any("test 1.0")))
	qt.Check(t, qt.Equals(h.PeerListenPort, 1234))
	qt.Check(t, qt.IsTrue(h.PeerPrefersEncryption))

	// A repeated handshake updates existing names, and new names up to the cap.
	newNames = RecvExtendedHandshake(&h, &pp.ExtendedHandshakeMessage{
		M: map[pp.ExtensionName]pp.ExtensionNumber{
			pp.ExtensionNamePex:      0,
			pp.ExtensionNameMetadata: 2,
		},
		Reqq: 10,
	}, 2)
	qt.Check(t, qt.DeepEquals(newNames, []pp.ExtensionName{pp.ExtensionNameMetadata}))
	qt.Check(t, qt.DeepEquals(h.PeerExtensionIDs, map[pp.ExtensionName]pp.ExtensionNumber{
		pp.ExtensionNamePex:      0,
		pp.ExtensionNameMetadata: 2,
	}))
	qt.Check(t, qt.Equals(h.PeerMaxRequests, 10))
	qt.Check(t, qt.Equals(h.PeerListenPort, 0))
	qt.Check(t, qt.IsFalse(h.PeerPrefersEncryption))

	newNames = RecvExtendedHandshake(&h, &pp.ExtendedHandshakeMessage{
		M: map[pp.ExtensionName]pp.ExtensionNumber{"ut_other": 3},
	}, 2)
	qt.Check(t, qt.HasLen(newNames, 0))
	qt.Check(t, qt.HasLen(h.PeerExtensionIDs, 2))
}
