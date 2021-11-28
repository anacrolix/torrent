package request_strategy

import (
	"encoding/gob"
	"testing"

	"github.com/RoaringBitmap/roaring"
	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp"
)

func init() {
	gob.Register(chunkIterRange(0))
	gob.Register(sliceChunksIter{})
}

type chunkIterRange ChunkIndex

func (me chunkIterRange) Iter(f func(ChunkIndex)) {
	for offset := ChunkIndex(0); offset < ChunkIndex(me); offset += 1 {
		f(offset)
	}
}

type sliceChunksIter []ChunkIndex

func chunkIter(offsets ...ChunkIndex) ChunksIter {
	return sliceChunksIter(offsets)
}

func (offsets sliceChunksIter) Iter(f func(ChunkIndex)) {
	for _, offset := range offsets {
		f(offset)
	}
}

func requestSetFromSlice(rs ...RequestIndex) (ret roaring.Bitmap) {
	ret.AddMany(rs)
	return
}

func init() {
	gob.Register(intPeerId(0))
}

type intPeerId int

func (i intPeerId) Uintptr() uintptr {
	return uintptr(i)
}

var hasAllRequests = func() (all roaring.Bitmap) {
	all.AddRange(0, roaring.MaxRange)
	return
}()

func checkNumRequestsAndInterest(c *qt.C, next PeerNextRequestState, num uint64, interest bool) {
	addressableBm := next.Requests
	c.Check(addressableBm.GetCardinality(), qt.ContentEquals, num)
	c.Check(next.Interested, qt.Equals, interest)
}

func checkResultsRequestsLen(t *testing.T, reqs roaring.Bitmap, l uint64) {
	qt.Check(t, reqs.GetCardinality(), qt.Equals, l)
}

var peerNextRequestStateChecker = qt.CmpEquals(
	cmp.Transformer(
		"bitmap",
		func(bm roaring.Bitmap) []uint32 {
			return bm.ToArray()
		}))
