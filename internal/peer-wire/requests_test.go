package peerWire

import (
	"testing"

	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

func TestInterest(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.Interested()))
	qt.Check(t, qt.IsFalse(s.SetInterested(false)))
	qt.Check(t, qt.IsTrue(s.SetInterested(true)))
	qt.Check(t, qt.IsTrue(s.Interested()))
	qt.Check(t, qt.IsFalse(s.SetInterested(true)))
}

func TestRequestThenChunk(t *testing.T) {
	var s State
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	qt.Check(t, qt.IsTrue(s.SentRequest(3)))
	qt.Check(t, qt.IsFalse(s.SentRequest(3)))
	qt.Check(t, qt.Equals(s.NumRequests(), 1))
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	requested, intended, err := s.RecvChunk(3)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// Only one chunk per request.
	_, _, err = s.RecvChunk(3)
	qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
	_, ok := s.RecvReject(3)
	qt.Check(t, qt.IsFalse(ok))
}

func TestUnrequestedChunk(t *testing.T) {
	var s State
	_, _, err := s.RecvChunk(0)
	qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
}

func TestDeletedRequestStillAcceptsChunk(t *testing.T) {
	var s State
	s.SentRequest(1)
	// For example, choked by a peer without the fast extension.
	qt.Check(t, qt.IsTrue(s.DeleteRequest(1)))
	qt.Check(t, qt.IsFalse(s.DeleteRequest(1)))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	requested, intended, err := s.RecvChunk(1)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsFalse(intended))
}

func TestCancelAwaitingReject(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.SentCancel(2, true)))
	s.SentRequest(2)
	qt.Check(t, qt.IsTrue(s.SentCancel(2, true)))
	qt.Check(t, qt.IsFalse(s.RequestOutstanding(2)))
	qt.Check(t, qt.IsTrue(s.RequestCancelled(2)))
	// Waiting for the ack still counts as something outstanding.
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	outstanding, ok := s.RecvReject(2)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsFalse(outstanding))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// The rejected chunk is no longer acceptable.
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(2)))
}

func TestCancelledChunkArrivesAnyway(t *testing.T) {
	var s State
	s.SentRequest(2)
	s.SentCancel(2, true)
	requested, intended, err := s.RecvChunk(2)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(2)))
	_, ok := s.RecvReject(2)
	qt.Check(t, qt.IsFalse(ok))
}

func TestCancelWithoutReject(t *testing.T) {
	var s State
	s.SentRequest(4)
	qt.Check(t, qt.IsTrue(s.SentCancel(4, false)))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(4)))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// It can be requested again straight away, and both chunks are accepted.
	qt.Check(t, qt.IsTrue(s.SentRequest(4)))
	for range 2 {
		_, _, err := s.RecvChunk(4)
		qt.Check(t, qt.IsNil(err))
	}
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(4)))
}

func TestRejectOutstanding(t *testing.T) {
	var s State
	s.SentRequest(5)
	outstanding, ok := s.RecvReject(5)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(5)))
}

func TestRequestsIterateInOrderAdded(t *testing.T) {
	var s State
	for _, r := range []RequestIndex{9, 2, 5} {
		s.SentRequest(r)
	}
	var got []RequestIndex
	s.RequestState().Requests.Iterate(func(r RequestIndex) bool {
		got = append(got, r)
		return true
	})
	qt.Check(t, qt.DeepEquals(got, []RequestIndex{9, 2, 5}))
}

func peerRequest(index int) Request {
	return Request{Index: pp.Integer(index), ChunkSpec: types.ChunkSpec{Length: 16 << 10}}
}

func TestCheckPeerRequest(t *testing.T) {
	s := NewState()
	r := peerRequest(0)
	qt.Check(t, qt.Equals(s.CheckPeerRequest(r, 2), PeerRequestWhileChoking))
	s.SetChoking(false)
	qt.Check(t, qt.Equals(s.CheckPeerRequest(r, 2), PeerRequestOk))
	s.AddPeerRequest(r)
	qt.Check(t, qt.Equals(s.CheckPeerRequest(r, 2), PeerRequestDuplicate))
	s.AddPeerRequest(peerRequest(1))
	qt.Check(t, qt.Equals(s.CheckPeerRequest(peerRequest(2), 2), PeerRequestQueueFull))
	// Duplicates are reported before choking or a full queue.
	s.SetChoking(true)
	qt.Check(t, qt.Equals(s.CheckPeerRequest(r, 2), PeerRequestDuplicate))
}

func TestPeerRequests(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.DeletePeerRequest(peerRequest(0))))
	s.AddPeerRequest(peerRequest(0))
	s.AddPeerRequest(peerRequest(1))
	s.AddPeerRequest(peerRequest(1))
	qt.Check(t, qt.Equals(s.NumPeerRequests(), 2))
	n := 0
	for range s.PeerRequests() {
		n++
	}
	qt.Check(t, qt.Equals(n, 2))
	qt.Check(t, qt.IsTrue(s.DeletePeerRequest(peerRequest(0))))
	qt.Check(t, qt.IsFalse(s.HavePeerRequest(peerRequest(0))))
	s.ClearPeerRequests()
	qt.Check(t, qt.Equals(s.NumPeerRequests(), 0))
}
