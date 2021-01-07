package torrent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultExtensionBytes(t *testing.T) {
	pex := defaultPeerExtensionBytes()
	assert.True(t, pex.SupportsDHT())
	assert.True(t, pex.SupportsExtended())
	assert.False(t, pex.GetBit(63))
	assert.Panics(t, func() { pex.GetBit(64) })
}
