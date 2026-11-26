package peerWire

import (
	"testing"

	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

func peerID(s string) (id types.PeerID) {
	copy(id[:], s)
	return
}

func TestRecvHandshake(t *testing.T) {
	var h Handshake
	res := pp.HandshakeResult{
		PeerExtensionBits: pp.NewPeerExtensionBytes(pp.ExtensionBitFast),
		PeerID:            peerID("-TR4000-abcdefghijkl"),
	}
	RecvHandshake(&h, res)
	qt.Check(t, qt.Equals(h.PeerID, types.PeerID(res.PeerID)))
	qt.Check(t, qt.IsTrue(h.PeerExtensionBytes.SupportsFast()))
}

func TestMeetsMinExtensions(t *testing.T) {
	have := pp.NewPeerExtensionBytes(pp.ExtensionBitFast, pp.ExtensionBitLtep)
	qt.Check(t, qt.IsTrue(MeetsMinExtensions(have, pp.PeerExtensionBits{})))
	qt.Check(t, qt.IsTrue(MeetsMinExtensions(have, pp.NewPeerExtensionBytes(pp.ExtensionBitFast))))
	qt.Check(t, qt.IsFalse(MeetsMinExtensions(have, pp.NewPeerExtensionBytes(pp.ExtensionBitDht))))
}

func TestRejectsCancels(t *testing.T) {
	for _, tc := range []struct {
		id   string
		fast bool
		want bool
	}{
		{"-TR3000-abcdefghijkl", true, false},
		{"-TR4000-abcdefghijkl", true, true},
		{"-TR4000-abcdefghijkl", false, false},
		{"-qB4500-abcdefghijkl", true, true},
		{"-qB4500-abcdefghijkl", false, false},
		// Not Transmission's scheme.
		{"-TR30000abcdefghijkl", true, true},
	} {
		qt.Check(t, qt.Equals(RejectsCancels(peerID(tc.id), tc.fast), tc.want), qt.Commentf("%v", tc))
	}
}

func TestUsesOwnExtensionIDs(t *testing.T) {
	qt.Check(t, qt.IsTrue(UsesOwnExtensionIDs(peerID("-SD0100-abcdefghijkl"))))
	qt.Check(t, qt.IsTrue(UsesOwnExtensionIDs(peerID("-XL0012-abcdefghijkl"))))
	qt.Check(t, qt.IsFalse(UsesOwnExtensionIDs(peerID("-TR4000-abcdefghijkl"))))
}
