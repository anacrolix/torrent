package peerWire

import (
	"net/netip"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestRendezvous(t *testing.T) {
	var s State
	target := netip.MustParseAddrPort("1.2.3.4:5")
	// Connects arrive unprompted when we're the target.
	qt.Check(t, qt.IsFalse(s.RecvHolepunchConnect(target)))
	// Errors only answer a rendezvous.
	qt.Check(t, qt.IsFalse(s.RecvHolepunchError(target)))
	qt.Assert(t, qt.IsTrue(s.SentRendezvous(target)))
	qt.Check(t, qt.IsTrue(s.RendezvousSent(target)))
	qt.Check(t, qt.IsTrue(s.RecvHolepunchConnect(target)))
	qt.Check(t, qt.IsFalse(s.RendezvousSent(target)))
	qt.Assert(t, qt.IsTrue(s.SentRendezvous(target)))
	qt.Check(t, qt.IsTrue(s.RecvHolepunchError(target)))
	qt.Check(t, qt.IsFalse(s.RecvHolepunchError(target)))
}

func TestRendezvousBounded(t *testing.T) {
	var s State
	for i := range MaxSentRendezvous {
		qt.Assert(t, qt.IsTrue(s.SentRendezvous(netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 0, 0, byte(i)}), 1))))
	}
	qt.Check(t, qt.IsFalse(s.SentRendezvous(netip.MustParseAddrPort("2.0.0.1:1"))))
	// Repeats of an awaiting target are still fine.
	qt.Check(t, qt.IsTrue(s.SentRendezvous(netip.MustParseAddrPort("1.0.0.0:1"))))
}
