package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestDefaultExtensionBytes(t *testing.T) {
	pex := defaultPeerExtensionBytes()
	qt.Check(t, qt.IsTrue(pex.SupportsDHT()))
	qt.Check(t, qt.IsTrue(pex.SupportsExtended()))
	qt.Check(t, qt.IsFalse(pex.GetBit(63)))
	qt.Check(t, qt.PanicMatches(func() { pex.GetBit(64) }, ".*"))
}
