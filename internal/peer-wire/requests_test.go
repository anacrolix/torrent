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

// A chunk of 16 KiB at index i in piece 0, as a fixed chunk layout would make it.
func chunk(i RequestIndex) Request {
	return Request{Index: 0, ChunkSpec: types.ChunkSpec{Begin: pp.Integer(i) * 16 << 10, Length: 16 << 10}}
}

func sendRequest(s *State, i RequestIndex) bool {
	return s.SentRequest(i, chunk(i))
}

func TestRequestThenChunk(t *testing.T) {
	var s State
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	qt.Check(t, qt.IsTrue(sendRequest(&s, 3)))
	qt.Check(t, qt.IsFalse(sendRequest(&s, 3)))
	qt.Check(t, qt.Equals(s.NumRequests(), 1))
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	r, requested, intended, err := s.RecvChunk(chunk(3))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(r, 3))
	qt.Check(t, qt.IsTrue(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// Only one chunk per request.
	_, _, _, err = s.RecvChunk(chunk(3))
	qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
	_, _, ok := s.RecvReject(chunk(3))
	qt.Check(t, qt.IsFalse(ok))
}

func TestUnrequestedChunk(t *testing.T) {
	var s State
	_, _, _, err := s.RecvChunk(chunk(0))
	qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
}

// Replies are matched on the exact index, begin and length that was requested (BEP 3 says
// pieces are correlated with requests implicitly, BEP 6 that Reject names the request).
func TestRepliesMustMatchRequestExactly(t *testing.T) {
	var s State
	sendRequest(&s, 1)
	want := chunk(1)
	for _, other := range []Request{
		{Index: want.Index, ChunkSpec: types.ChunkSpec{Begin: want.Begin + 1, Length: want.Length}},
		{Index: want.Index, ChunkSpec: types.ChunkSpec{Begin: want.Begin, Length: want.Length - 1}},
		{Index: want.Index + 1, ChunkSpec: want.ChunkSpec},
	} {
		_, _, ok := s.RecvReject(other)
		qt.Check(t, qt.IsFalse(ok))
		_, _, _, err := s.RecvChunk(other)
		qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
	}
	qt.Check(t, qt.IsTrue(s.RequestOutstanding(1)))
	r, outstanding, ok := s.RecvReject(want)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
	qt.Check(t, qt.Equals(r, 1))
}

func TestDeletedRequestStillAcceptsChunk(t *testing.T) {
	var s State
	sendRequest(&s, 1)
	// For example, choked by a peer without the fast extension.
	qt.Check(t, qt.IsTrue(s.DeleteRequest(1, chunk(1))))
	qt.Check(t, qt.IsFalse(s.DeleteRequest(1, chunk(1))))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// There's nothing left to reject.
	_, _, ok := s.RecvReject(chunk(1))
	qt.Check(t, qt.IsFalse(ok))
	_, requested, intended, err := s.RecvChunk(chunk(1))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsFalse(intended))
}

func TestCancelAwaitingReject(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.SentCancel(2, chunk(2), true)))
	sendRequest(&s, 2)
	qt.Check(t, qt.IsTrue(s.SentCancel(2, chunk(2), true)))
	qt.Check(t, qt.IsFalse(s.RequestOutstanding(2)))
	qt.Check(t, qt.IsTrue(s.RequestCancelled(2)))
	// Waiting for the ack still counts as something outstanding.
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	r, outstanding, ok := s.RecvReject(chunk(2))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsFalse(outstanding))
	qt.Check(t, qt.Equals(r, 2))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// The rejected chunk is no longer acceptable.
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(2))))
}

func TestCancelledChunkArrivesAnyway(t *testing.T) {
	var s State
	sendRequest(&s, 2)
	s.SentCancel(2, chunk(2), true)
	_, requested, intended, err := s.RecvChunk(chunk(2))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(2)))
	// BEP 6: a cancelled request gets one reply, the Reject or the Piece.
	_, _, ok := s.RecvReject(chunk(2))
	qt.Check(t, qt.IsFalse(ok))
}

func TestCancelWithoutReject(t *testing.T) {
	var s State
	sendRequest(&s, 4)
	qt.Check(t, qt.IsTrue(s.SentCancel(4, chunk(4), false)))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(4)))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// It can be requested again straight away, and both chunks are accepted.
	qt.Check(t, qt.IsTrue(sendRequest(&s, 4)))
	for range 2 {
		_, _, _, err := s.RecvChunk(chunk(4))
		qt.Check(t, qt.IsNil(err))
	}
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(4))))
}

func TestRejectOutstanding(t *testing.T) {
	var s State
	sendRequest(&s, 5)
	_, outstanding, ok := s.RecvReject(chunk(5))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(5))))
}

func TestSentRequestMismatchPanics(t *testing.T) {
	var s State
	sendRequest(&s, 1)
	qt.Check(t, qt.PanicMatches(func() { s.DeleteRequest(1, chunk(2)) }, ".*"))
}

func TestRequestsIterateInOrderAdded(t *testing.T) {
	var s State
	for _, r := range []RequestIndex{9, 2, 5} {
		sendRequest(&s, r)
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
