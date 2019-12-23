package torrent

import (
	"net"
	"testing"

	"github.com/google/btree"
	"github.com/stretchr/testify/assert"
)

func TestPrioritizedPeers(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(p Peer) peerPriority {
			return bep40PriorityIgnoreError(p.addr(), IpPort{IP: net.ParseIP("0.0.0.0")})
		},
	}
	_, ok := pp.DeleteMin()
	assert.Panics(t, func() { pp.PopMax() })
	assert.False(t, ok)
	ps := []Peer{
		{IP: net.ParseIP("1.2.3.4")},
		{IP: net.ParseIP("1::2")},
		{IP: net.ParseIP("")},
		{IP: net.ParseIP(""), Trusted: true},
	}
	for i, p := range ps {
		t.Logf("peer %d priority: %08x trusted: %t\n", i, pp.getPrio(p), p.Trusted)
		assert.False(t, pp.Add(p))
		assert.True(t, pp.Add(p))
		assert.Equal(t, i+1, pp.Len())
	}
	pop := func(expected *Peer) {
		if expected == nil {
			assert.Panics(t, func() { pp.PopMax() })
		} else {
			assert.Equal(t, *expected, pp.PopMax())
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
