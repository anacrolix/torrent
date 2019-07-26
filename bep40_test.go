package torrent

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBep40Priority(t *testing.T) {
	assert.EqualValues(t, peerPriority(0xec2d7224), bep40PriorityIgnoreError(
		IpPort{net.ParseIP("123.213.32.10"), 0},
		IpPort{net.ParseIP("98.76.54.32"), 0},
	))
	assert.EqualValues(t, peerPriority(0xec2d7224), bep40PriorityIgnoreError(
		IpPort{net.ParseIP("98.76.54.32"), 0},
		IpPort{net.ParseIP("123.213.32.10"), 0},
	))
	assert.Equal(t, peerPriority(0x99568189), bep40PriorityIgnoreError(
		IpPort{net.ParseIP("123.213.32.10"), 0},
		IpPort{net.ParseIP("123.213.32.234"), 0},
	))
	assert.EqualValues(t, "\x00\x00\x00\x00", func() []byte {
		b, _ := bep40PriorityBytes(
			IpPort{net.ParseIP("123.213.32.234"), 0},
			IpPort{net.ParseIP("123.213.32.234"), 0},
		)
		return b
	}())
}
