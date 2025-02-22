package containers

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"

	"github.com/anacrolix/dht/v2/int160"
	"github.com/anacrolix/dht/v2/internal/testutil"
)

func TestSampleAddrsDiffer(t *testing.T) {
	c := qt.New(t)
	for i, a := range testutil.SampleAddrMaybeIds {
		for j, b := range testutil.SampleAddrMaybeIds[i+1:] {
			c.Assert(a, qt.Not(qt.Equals), b,
				qt.Commentf("%v, %v", i, j+i+1))
		}
	}
}

func TestNodesByDistance(t *testing.T) {
	a := NewImmutableAddrMaybeIdsByDistance(int160.T{})
	push := func(i int) {
		a = a.Add(testutil.SampleAddrMaybeIds[i])
	}
	push(4)
	c := qt.New(t)
	c.Assert(a.Len(), qt.Equals, 1)
	push(2)
	c.Assert(a.Len(), qt.Equals, 2)
	push(0)
	c.Assert(a.Len(), qt.Equals, 3)
	push(3)
	c.Assert(a.Len(), qt.Equals, 4)
	push(0)
	c.Assert(a.Len(), qt.Equals, 4)
	push(1)
	c.Assert(a.Len(), qt.Equals, 5)
	pop := func(is ...int) {
		ok := a.Len() != 0
		assert.True(t, ok)
		first := a.Next()
		assert.Contains(t, func() (ret []addrMaybeId) {
			for _, i := range is {
				ret = append(ret, testutil.SampleAddrMaybeIds[i])
			}
			return
		}(), first)
		a = a.Delete(first)
	}
	pop(1)
	pop(2)
	pop(3)
	pop(0, 4)
	pop(0, 4)
	// pop(0, 4)
	assert.EqualValues(t, 0, a.Len())
}
