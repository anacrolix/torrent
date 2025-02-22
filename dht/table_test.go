package dht

import (
	"net"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"

	"github.com/anacrolix/dht/v2/int160"
)

func TestTable(t *testing.T) {
	tbl := table{k: 8}
	var maxFar int160.T
	maxFar.SetMax()
	assert.Equal(t, 0, tbl.bucketIndex(maxFar))
	assert.Panics(t, func() { tbl.bucketIndex(tbl.rootID) })

	assert.Error(t, tbl.addNode(&node{}))
	assert.Equal(t, 0, tbl.buckets[0].Len())

	id0 := int160.FromByteString("\x2f\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	id1 := int160.FromByteString("\x2e\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	n0 := &node{nodeKey: nodeKey{
		Id:   id0,
		Addr: NewAddr(&net.UDPAddr{}),
	}}
	n1 := &node{nodeKey: nodeKey{
		Id:   id1,
		Addr: NewAddr(&net.UDPAddr{}),
	}}

	assert.NoError(t, tbl.addNode(n0))
	assert.Equal(t, 1, tbl.buckets[2].Len())

	assert.Error(t, tbl.addNode(n0))
	assert.Equal(t, 1, tbl.buckets[2].Len())
	assert.Equal(t, 1, tbl.numNodes())

	assert.NoError(t, tbl.addNode(n1))
	assert.Equal(t, 2, tbl.buckets[2].Len())
	assert.Equal(t, 2, tbl.numNodes())

	tbl.dropNode(n0)
	assert.Equal(t, 1, tbl.buckets[2].Len())
	assert.Equal(t, 1, tbl.numNodes())

	tbl.dropNode(n1)
	assert.Equal(t, 0, tbl.buckets[2].Len())
	assert.Equal(t, 0, tbl.numNodes())
}

func TestRandomIdInBucket(t *testing.T) {
	tbl := table{
		rootID: int160.FromByteArray(RandomNodeID()),
	}
	t.Logf("%v: table root id", tbl.rootID)
	for i := range tbl.buckets {
		id := tbl.randomIdForBucket(i)
		t.Logf("%v: random id for bucket index %v", id, i)
		qt.Assert(t, tbl.bucketIndex(id), qt.Equals, i)
	}
}
