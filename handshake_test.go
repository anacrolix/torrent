package torrent

import (
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/stretchr/testify/assert"
)

func TestDefaultExtensionBytes(t *testing.T) {
	var pex peerExtensionBytes
	missinggo.CopyExact(&pex, defaultExtensionBytes)
	assert.True(t, pex.SupportsDHT())
	assert.True(t, pex.SupportsExtended())
	assert.False(t, pex.SupportsFast())
}
