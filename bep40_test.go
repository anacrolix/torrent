package torrent

import (
	"net/netip"
	"testing"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/stretchr/testify/assert"
)

func TestBep40Priority(t *testing.T) {
	assert.EqualValues(t, peerPriority(0xec2d7224), bep40PriorityIgnoreError(
		errorsx.Must(netip.ParseAddrPort("123.213.32.10:0")),
		errorsx.Must(netip.ParseAddrPort("98.76.54.32:0")),
	))
	assert.EqualValues(t, peerPriority(0xec2d7224), bep40PriorityIgnoreError(
		errorsx.Must(netip.ParseAddrPort("98.76.54.32:0")),
		errorsx.Must(netip.ParseAddrPort("123.213.32.10:0")),
	))
	assert.Equal(t, peerPriority(0x99568189), bep40PriorityIgnoreError(
		errorsx.Must(netip.ParseAddrPort("123.213.32.10:0")),
		errorsx.Must(netip.ParseAddrPort("123.213.32.234:0")),
	))
	assert.EqualValues(t, "\x00\x00\x00\x00", func() []byte {
		b, _ := bep40PriorityBytes(
			errorsx.Must(netip.ParseAddrPort("123.213.32.234:0")),
			errorsx.Must(netip.ParseAddrPort("123.213.32.234:0")),
		)
		return b
	}())
}
