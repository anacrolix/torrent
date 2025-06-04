package torrent

import (
	"net"
	"net/netip"
	"testing"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrioritizedPeers(t *testing.T) {
	pp := newPeerPool(3, func(p Peer) peerPriority {
		return bep40PriorityIgnoreError(p.addr(), errorsx.Must(netip.ParseAddrPort("0.0.0.0:0")))
	})
	_, ok := pp.DeleteMin()
	assert.False(t, ok)
	_, ok = pp.PopMax()
	assert.False(t, ok)

	ps := []Peer{
		{IP: net.ParseIP("1.2.3.4")},
		{IP: net.ParseIP("1::2")},
		{IP: net.ParseIP("")},
		{IP: net.ParseIP(""), Trusted: true},
	}
	for i, p := range ps {
		// log.Printf("peer %d priority: %08x trusted: %t - %v\n", i, pp.getPrio(p), p.Trusted, p.addr())
		require.False(t, pp.Add(p))
		require.True(t, pp.Add(p))
		require.Equal(t, i+1, pp.Len())
	}
	pop := func(expected *Peer) {
		if expected == nil {
			_, ok := pp.PopMax()
			assert.False(t, ok)
		} else {
			actual, ok := pp.PopMax()
			assert.True(t, ok)
			assert.Equal(t, *expected, actual)
		}
	}
	min := func(expected *Peer) {
		i, ok := pp.DeleteMin()
		if expected == nil {
			assert.False(t, ok)
		} else {
			assert.True(t, ok)
			assert.Equal(t, *expected, i.p)
		}
	}
	pop(&ps[3])
	pop(&ps[1])
	min(&ps[2])
	pop(&ps[0])
	min(nil)
	pop(nil)
}
