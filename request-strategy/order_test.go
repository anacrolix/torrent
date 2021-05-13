package request_strategy

import (
	"math"
	"testing"

	pp "github.com/anacrolix/torrent/peer_protocol"
	qt "github.com/frankban/quicktest"
)

func r(i pieceIndex, begin int) Request {
	return Request{pp.Integer(i), ChunkSpec{pp.Integer(begin), 1}}
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

func TestStealingFromSlowerPeers(t *testing.T) {
	c := qt.New(t)
	order := ClientPieceOrder{}
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
	c.Assert(order.DoRequests([]*Torrent{{
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
	}}), qt.ContentEquals, map[PeerId]PeerNextRequestState{
		intPeerId(2): {
			Interested: true,
			Requests:   requestSetFromSlice(r(0, 0)),
		},
		intPeerId(3): {
			Interested: true,
			Requests:   requestSetFromSlice(r(0, 1)),
		},
		stealee.Id: {
			Interested: false,
			Requests:   requestSetFromSlice(),
		},
	})
}
