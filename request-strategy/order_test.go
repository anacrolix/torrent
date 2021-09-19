package request_strategy

import (
	"math"
	"testing"

	"github.com/RoaringBitmap/roaring"
	qt "github.com/frankban/quicktest"
	"github.com/google/go-cmp/cmp"
)

func chunkIterRange(end ChunkIndex) ChunksIter {
	return func(f func(ChunkIndex)) {
		for offset := ChunkIndex(0); offset < end; offset += 1 {
			f(offset)
		}
	}
}

func chunkIter(offsets ...ChunkIndex) ChunksIter {
	return func(f func(ChunkIndex)) {
		for _, offset := range offsets {
			f(offset)
		}
	}
}

func requestSetFromSlice(rs ...RequestIndex) (ret roaring.Bitmap) {
	ret.AddMany(rs)
	return
}

type intPeerId int

func (i intPeerId) Uintptr() uintptr {
	return uintptr(i)
}

func hasAllRequests(RequestIndex) bool { return true }

func TestStealingFromSlowerPeer(t *testing.T) {
	c := qt.New(t)
	basePeer := Peer{
		HasPiece: func(i pieceIndex) bool {
			return true
		},
		MaxRequests:  math.MaxInt16,
		DownloadRate: 2,
	}
	// Slower than the stealers, but has all requests already.
	stealee := basePeer
	stealee.DownloadRate = 1
	stealee.HasExistingRequest = hasAllRequests
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
		ChunksPerPiece: 9,
		Pieces: []Piece{{
			Request:           true,
			NumPendingChunks:  5,
			IterPendingChunks: chunkIterRange(5),
		}},
		Peers: []Peer{
			stealee,
			firstStealer,
			secondStealer,
		},
	}}})

	c.Assert(results, qt.HasLen, 3)
	check := func(p PeerId, l uint64) {
		addressableBm := results[p].Requests
		c.Check(addressableBm.GetCardinality(), qt.ContentEquals, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(stealee.Id, 1)
	check(firstStealer.Id, 2)
	check(secondStealer.Id, 2)
}

func checkNumRequestsAndInterest(c *qt.C, next PeerNextRequestState, num uint64, interest bool) {
	addressableBm := next.Requests
	c.Check(addressableBm.GetCardinality(), qt.ContentEquals, num)
	c.Check(next.Interested, qt.Equals, interest)
}

func TestStealingFromSlowerPeersBasic(t *testing.T) {
	c := qt.New(t)
	basePeer := Peer{
		HasPiece: func(i pieceIndex) bool {
			return true
		},
		MaxRequests:  math.MaxInt16,
		DownloadRate: 2,
	}
	stealee := basePeer
	stealee.DownloadRate = 1
	stealee.HasExistingRequest = hasAllRequests
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
		ChunksPerPiece: 9,
		Pieces: []Piece{{
			Request:           true,
			NumPendingChunks:  2,
			IterPendingChunks: chunkIter(0, 1),
		}},
		Peers: []Peer{
			stealee,
			firstStealer,
			secondStealer,
		},
	}}})

	checkNumRequestsAndInterest(c, results[firstStealer.Id], 1, true)
	checkNumRequestsAndInterest(c, results[secondStealer.Id], 1, true)
	checkNumRequestsAndInterest(c, results[stealee.Id], 0, false)
}

func checkResultsRequestsLen(t *testing.T, reqs roaring.Bitmap, l uint64) {
	qt.Check(t, reqs.GetCardinality(), qt.Equals, l)
}

func TestPeerKeepsExistingIfReasonable(t *testing.T) {
	c := qt.New(t)
	basePeer := Peer{
		HasPiece: func(i pieceIndex) bool {
			return true
		},
		MaxRequests:  math.MaxInt16,
		DownloadRate: 2,
	}
	// Slower than the stealers, but has all requests already.
	stealee := basePeer
	stealee.DownloadRate = 1
	keepReq := RequestIndex(0)
	stealee.HasExistingRequest = func(r RequestIndex) bool {
		return r == keepReq
	}
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
		ChunksPerPiece: 9,
		Pieces: []Piece{{
			Request:           true,
			NumPendingChunks:  4,
			IterPendingChunks: chunkIter(0, 1, 3, 4),
		}},
		Peers: []Peer{
			stealee,
			firstStealer,
			secondStealer,
		},
	}}})

	c.Assert(results, qt.HasLen, 3)
	check := func(p PeerId, l uint64) {
		checkResultsRequestsLen(t, results[p].Requests, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(firstStealer.Id, 2)
	check(secondStealer.Id, 1)
	c.Check(
		results[stealee.Id],
		peerNextRequestStateChecker,
		PeerNextRequestState{
			Interested: true,
			Requests:   requestSetFromSlice(keepReq),
		},
	)
}

var peerNextRequestStateChecker = qt.CmpEquals(
	cmp.Transformer(
		"bitmap",
		func(bm roaring.Bitmap) []uint32 {
			return bm.ToArray()
		}))

func TestDontStealUnnecessarily(t *testing.T) {
	c := qt.New(t)
	basePeer := Peer{
		HasPiece: func(i pieceIndex) bool {
			return true
		},
		MaxRequests:  math.MaxInt16,
		DownloadRate: 2,
	}
	// Slower than the stealers, but has all requests already.
	stealee := basePeer
	stealee.DownloadRate = 1
	r := func(i, c RequestIndex) RequestIndex {
		return i*9 + c
	}
	keepReqs := requestSetFromSlice(
		r(3, 2), r(3, 4), r(3, 6), r(3, 8),
		r(4, 0), r(4, 1), r(4, 7), r(4, 8))
	stealee.HasExistingRequest = func(r RequestIndex) bool {
		return keepReqs.Contains(r)
	}
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	secondStealer.HasPiece = func(i pieceIndex) bool {
		switch i {
		case 1, 3:
			return true
		default:
			return false
		}
	}
	results := Run(Input{Torrents: []Torrent{{
		ChunksPerPiece: 9,
		Pieces: []Piece{
			{
				Request:           true,
				NumPendingChunks:  0,
				IterPendingChunks: chunkIterRange(9),
			},
			{
				Request:           true,
				NumPendingChunks:  7,
				IterPendingChunks: chunkIterRange(7),
			},
			{
				Request:           true,
				NumPendingChunks:  0,
				IterPendingChunks: chunkIterRange(0),
			},
			{
				Request:           true,
				NumPendingChunks:  9,
				IterPendingChunks: chunkIterRange(9),
			},
			{
				Request:           true,
				NumPendingChunks:  9,
				IterPendingChunks: chunkIterRange(9),
			}},
		Peers: []Peer{
			firstStealer,
			stealee,
			secondStealer,
		},
	}}})

	c.Assert(results, qt.HasLen, 3)
	check := func(p PeerId, l uint64) {
		checkResultsRequestsLen(t, results[p].Requests, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(firstStealer.Id, 5)
	check(secondStealer.Id, 7+9)
	c.Check(
		results[stealee.Id],
		peerNextRequestStateChecker,
		PeerNextRequestState{
			Interested: true,
			Requests:   requestSetFromSlice(r(4, 0), r(4, 1), r(4, 7), r(4, 8)),
		},
	)
}

// This tests a situation where multiple peers had the same existing request, due to "actual" and
// "next" request states being out of sync. This reasonable occurs when a peer hasn't fully updated
// its actual request state since the last request strategy run.
func TestDuplicatePreallocations(t *testing.T) {
	peer := func(id int, downloadRate float64) Peer {
		return Peer{
			HasExistingRequest: hasAllRequests,
			MaxRequests:        2,
			HasPiece: func(i pieceIndex) bool {
				return true
			},
			Id:           intPeerId(id),
			DownloadRate: downloadRate,
		}
	}
	results := Run(Input{
		Torrents: []Torrent{{
			ChunksPerPiece: 1,
			Pieces: []Piece{{
				Request:           true,
				NumPendingChunks:  1,
				IterPendingChunks: chunkIterRange(1),
			}, {
				Request:           true,
				NumPendingChunks:  1,
				IterPendingChunks: chunkIterRange(1),
			}},
			Peers: []Peer{
				// The second peer was be marked as the preallocation, clobbering the first. The
				// first peer is preferred, and the piece isn't striped, so it gets preallocated a
				// request, and then gets reallocated from the peer the same request.
				peer(1, 2),
				peer(2, 1),
			},
		}},
	})
	c := qt.New(t)
	req1 := results[intPeerId(1)].Requests
	req2 := results[intPeerId(2)].Requests
	c.Assert(uint64(2), qt.Equals, req1.GetCardinality()+req2.GetCardinality())
}
