package request_strategy

import (
	"math"
	"testing"

	"github.com/bradfitz/iter"
	qt "github.com/frankban/quicktest"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func r(i pieceIndex, begin int) Request {
	return Request{pp.Integer(i), ChunkSpec{pp.Integer(begin), 1}}
}

func chunkIterRange(end int) func(func(ChunkSpec)) {
	return func(f func(ChunkSpec)) {
		for offset := range iter.N(end) {
			f(ChunkSpec{pp.Integer(offset), 1})
		}
	}
}

func chunkIter(offsets ...int) func(func(ChunkSpec)) {
	return func(f func(ChunkSpec)) {
		for _, offset := range offsets {
			f(ChunkSpec{pp.Integer(offset), 1})
		}
	}
}

func requestSetFromSlice(rs ...Request) (ret map[Request]struct{}) {
	ret = make(map[Request]struct{}, len(rs))
	for _, r := range rs {
		ret[r] = struct{}{}
	}
	return
}

type intPeerId int

func (i intPeerId) Uintptr() uintptr {
	return uintptr(i)
}

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
	stealee.HasExistingRequest = func(r Request) bool {
		return true
	}
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
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
	check := func(p PeerId, l int) {
		c.Check(results[p].Requests, qt.HasLen, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(stealee.Id, 1)
	check(firstStealer.Id, 2)
	check(secondStealer.Id, 2)
}

func checkNumRequestsAndInterest(c *qt.C, next PeerNextRequestState, num int, interest bool) {
	c.Check(next.Requests, qt.HasLen, num)
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
	stealee.HasExistingRequest = func(r Request) bool {
		return true
	}
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
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
	keepReq := r(0, 0)
	stealee.HasExistingRequest = func(r Request) bool {
		return r == keepReq
	}
	stealee.Id = intPeerId(1)
	firstStealer := basePeer
	firstStealer.Id = intPeerId(2)
	secondStealer := basePeer
	secondStealer.Id = intPeerId(3)
	results := Run(Input{Torrents: []Torrent{{
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
	check := func(p PeerId, l int) {
		c.Check(results[p].Requests, qt.HasLen, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(firstStealer.Id, 2)
	check(secondStealer.Id, 1)
	c.Check(results[stealee.Id], qt.ContentEquals, PeerNextRequestState{
		Interested: true,
		Requests:   requestSetFromSlice(keepReq),
	})
}

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
	keepReqs := requestSetFromSlice(
		r(3, 2), r(3, 4), r(3, 6), r(3, 8),
		r(4, 0), r(4, 1), r(4, 7), r(4, 8))
	stealee.HasExistingRequest = func(r Request) bool {
		_, ok := keepReqs[r]
		return ok
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
	check := func(p PeerId, l int) {
		c.Check(results[p].Requests, qt.HasLen, l)
		c.Check(results[p].Interested, qt.Equals, l > 0)
	}
	check(firstStealer.Id, 5)
	check(secondStealer.Id, 7+9)
	c.Check(results[stealee.Id], qt.ContentEquals, PeerNextRequestState{
		Interested: true,
		Requests:   requestSetFromSlice(r(4, 0), r(4, 1), r(4, 7), r(4, 8)),
	})
}

// This tests a situation where multiple peers had the same existing request, due to "actual" and
// "next" request states being out of sync. This reasonable occurs when a peer hasn't fully updated
// its actual request state since the last request strategy run.
func TestDuplicatePreallocations(t *testing.T) {
	peer := func(id int, downloadRate float64) Peer {
		return Peer{
			HasExistingRequest: func(r Request) bool {
				return true
			},
			MaxRequests: 2,
			HasPiece: func(i pieceIndex) bool {
				return true
			},
			Id:           intPeerId(id),
			DownloadRate: downloadRate,
		}
	}
	results := Run(Input{
		Torrents: []Torrent{{
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
	c.Assert(2, qt.Equals, len(results[intPeerId(1)].Requests)+len(results[intPeerId(2)].Requests))
}
