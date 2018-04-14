package torrent

import (
	"net"
	"sort"
	"testing"

	"github.com/google/btree"
	"github.com/stretchr/testify/assert"
)

func TestPrioritizedPeers(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(p Peer) peerPriority {
			return bep40PriorityIgnoreError(p.addr(), ipPort{IP: net.ParseIP("0.0.0.0")})
		},
	}
	_, ok := pp.DeleteMin()
	assert.Panics(t, func() { pp.PopMax() })
	assert.False(t, ok)
	ps := []Peer{
		{IP: net.ParseIP("1.2.3.4")},
		{IP: net.ParseIP("1::2")},
		{IP: net.ParseIP("")},
	}
	for i, p := range ps {
		t.Logf("peer %d priority: %08x\n", i, pp.getPrio(p))
		assert.False(t, pp.Add(p))
		assert.True(t, pp.Add(p))
		assert.Equal(t, i+1, pp.Len())
	}
	sort.Slice(ps, func(i, j int) bool {
		return pp.getPrio(ps[i]) < pp.getPrio(ps[j])
	})
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
	pop(&ps[2])
	min(&ps[0])
	pop(&ps[1])
	min(nil)
	pop(nil)
}
