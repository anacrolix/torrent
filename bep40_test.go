package torrent

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBep40Priority(t *testing.T) {
	assert.EqualValues(t, 0xec2d7224, bep40Priority(
		ipPort{net.ParseIP("123.213.32.10"), 0},
		ipPort{net.ParseIP("98.76.54.32"), 0},
	))
	assert.EqualValues(t, 0xec2d7224, bep40Priority(
		ipPort{net.ParseIP("98.76.54.32"), 0},
		ipPort{net.ParseIP("123.213.32.10"), 0},
	))
	assert.Equal(t, peerPriority(0x99568189), bep40Priority(
		ipPort{net.ParseIP("123.213.32.10"), 0},
		ipPort{net.ParseIP("123.213.32.234"), 0},
	))
	assert.EqualValues(t, "\x00\x00\x00\x00", bep40PriorityBytes(
		ipPort{net.ParseIP("123.213.32.234"), 0},
		ipPort{net.ParseIP("123.213.32.234"), 0},
	))
}
