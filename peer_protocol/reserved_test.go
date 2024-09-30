package peer_protocol

import (
	"testing"

	"github.com/anacrolix/torrent/internal/qtnew"
	qt "github.com/go-quicktest/qt"
)

func TestV2BitLocation(t *testing.T) {
	var bits PeerExtensionBits
	bits.SetBit(ExtensionBitV2Upgrade, true)
	c := qtnew.New(t)
	qt.Assert(t, qt.Equals(bits[7], byte(0x10)))
}
