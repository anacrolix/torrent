package peer_protocol

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestV2BitLocation(t *testing.T) {
	var bits PeerExtensionBits
	bits.SetBit(ExtensionBitV2Upgrade, true)
	qt.Assert(t, qt.Equals(bits[7], byte(0x10)))
}
