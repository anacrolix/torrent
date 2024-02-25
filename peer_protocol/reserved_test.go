package peer_protocol

import (
	qt "github.com/frankban/quicktest"
	"testing"
)

func TestV2BitLocation(t *testing.T) {
	var bits PeerExtensionBits
	bits.SetBit(ExtensionBitV2Upgrade, true)
	c := qt.New(t)
	c.Assert(bits[7], qt.Equals, byte(0x10))
}
