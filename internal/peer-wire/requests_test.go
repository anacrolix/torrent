package peerWire

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

func TestInterest(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.Interested()))
	qt.Check(t, qt.IsFalse(s.SentInterested(false)))
	qt.Check(t, qt.IsTrue(s.SentInterested(true)))
	qt.Check(t, qt.IsTrue(s.Interested()))
	qt.Check(t, qt.IsFalse(s.SentInterested(true)))
}

// A chunk of 16 KiB at index i in piece 0, as a fixed chunk layout would make it.
func chunk(i int) Request {
	return Request{Index: 0, ChunkSpec: types.ChunkSpec{Begin: pp.Integer(i) * 16 << 10, Length: 16 << 10}}
}

var fastBits = pp.NewPeerExtensionBytes(pp.ExtensionBitFast)

// A state after a handshake in which both sides set the fast extension bit.
func fastState() State {
	s := NewState()
	s.Handshook(fastBits, Handshake{PeerExtensionBytes: fastBits})
	return s
}

func TestHandshook(t *testing.T) {
	s := NewState()
	qt.Check(t, qt.IsFalse(s.Fast()))
	// Both sides must set it.
	s.Handshook(fastBits, Handshake{})
	qt.Check(t, qt.IsFalse(s.Fast()))
	qt.Check(t, qt.IsFalse(s.ExpectsCancelReplies()))
	s.Handshook(fastBits, Handshake{PeerExtensionBytes: fastBits})
	qt.Check(t, qt.IsTrue(s.Fast()))
	qt.Check(t, qt.IsTrue(s.ExpectsCancelReplies()))
	// Transmission before 4.0 doesn't reply to cancels.
	s.Handshook(fastBits, Handshake{PeerExtensionBytes: fastBits, PeerID: peerID("-TR3000-abcdefghijkl")})
	qt.Check(t, qt.IsTrue(s.Fast()))
	qt.Check(t, qt.IsFalse(s.ExpectsCancelReplies()))
}

func sendRequest(s *State, i int) bool {
	return s.SentRequest(chunk(i))
}

func TestRequestThenChunk(t *testing.T) {
	var s State
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	qt.Check(t, qt.IsTrue(sendRequest(&s, 3)))
	qt.Check(t, qt.IsFalse(sendRequest(&s, 3)))
	qt.Check(t, qt.Equals(s.NumRequests(), 1))
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	requested, intended, err := s.RecvChunk(chunk(3))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// Only one chunk per request.
	_, _, err = s.RecvChunk(chunk(3))
	qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
	_, ok := s.RecvReject(chunk(3))
	qt.Check(t, qt.IsFalse(ok))
}

func TestUnrequestedChunk(t *testing.T) {
	var s State
	_, _, err := s.RecvChunk(chunk(0))
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
		_, ok := s.RecvReject(other)
		qt.Check(t, qt.IsFalse(ok))
		_, _, err := s.RecvChunk(other)
		qt.Check(t, qt.ErrorIs(err, ErrUnexpectedChunk))
	}
	qt.Check(t, qt.IsTrue(s.RequestOutstanding(chunk(1))))
	outstanding, ok := s.RecvReject(want)
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
}

func TestChokeWithoutFastDeletesRequests(t *testing.T) {
	s := NewState()
	s.RecvUnchoke()
	sendRequest(&s, 1)
	sendRequest(&s, 2)
	changed, deleted := s.RecvChoke()
	qt.Check(t, qt.IsTrue(changed))
	slices.SortFunc(deleted, func(a, b Request) int { return int(a.Begin) - int(b.Begin) })
	qt.Check(t, qt.DeepEquals(deleted, []Request{chunk(1), chunk(2)}))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// Repeated chokes change nothing.
	changed, deleted = s.RecvChoke()
	qt.Check(t, qt.IsFalse(changed))
	qt.Check(t, qt.HasLen(deleted, 0))
	// There's nothing left to reject, but the chunks may already be on their way.
	_, ok := s.RecvReject(chunk(1))
	qt.Check(t, qt.IsFalse(ok))
	requested, intended, err := s.RecvChunk(chunk(1))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsFalse(intended))
}

func TestChokeWithFastKeepsRequests(t *testing.T) {
	s := fastState()
	s.RecvUnchoke()
	sendRequest(&s, 1)
	changed, deleted := s.RecvChoke()
	qt.Check(t, qt.IsTrue(changed))
	qt.Check(t, qt.HasLen(deleted, 0))
	qt.Check(t, qt.IsTrue(s.RequestOutstanding(chunk(1))))
	// The peer still owes a reply.
	outstanding, ok := s.RecvReject(chunk(1))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
}

func TestClosedDeletesRequests(t *testing.T) {
	s := fastState()
	sendRequest(&s, 1)
	sendRequest(&s, 2)
	s.SentCancel(chunk(2))
	s.SentUnchoke()
	s.RecvRequest(peerRequest(0), 1)
	deleted := s.Closed()
	qt.Check(t, qt.DeepEquals(deleted, []Request{chunk(1)}))
	qt.Check(t, qt.Equals(s.NumRequests(), 0))
	qt.Check(t, qt.Equals(s.NumPeerRequests(), 0))
	qt.Check(t, qt.HasLen(s.Closed(), 0))
	// The chunk may still arrive.
	_, _, err := s.RecvChunk(chunk(1))
	qt.Check(t, qt.IsNil(err))
}

func TestCancelAwaitingReject(t *testing.T) {
	s := fastState()
	qt.Check(t, qt.IsFalse(s.SentCancel(chunk(2))))
	sendRequest(&s, 2)
	qt.Check(t, qt.IsTrue(s.SentCancel(chunk(2))))
	qt.Check(t, qt.IsFalse(s.RequestOutstanding(chunk(2))))
	qt.Check(t, qt.IsTrue(s.RequestCancelled(chunk(2))))
	// Waiting for the ack still counts as something outstanding.
	qt.Check(t, qt.IsFalse(s.LowOnRequests()))
	outstanding, ok := s.RecvReject(chunk(2))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsFalse(outstanding))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// The rejected chunk is no longer acceptable.
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(2))))
}

func TestCancelledChunkArrivesAnyway(t *testing.T) {
	s := fastState()
	sendRequest(&s, 2)
	s.SentCancel(chunk(2))
	requested, intended, err := s.RecvChunk(chunk(2))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(requested))
	qt.Check(t, qt.IsTrue(intended))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(chunk(2))))
	// BEP 6: a cancelled request gets one reply, the Reject or the Piece.
	_, ok := s.RecvReject(chunk(2))
	qt.Check(t, qt.IsFalse(ok))
}

func TestCancelWithoutReject(t *testing.T) {
	// Without the fast extension, cancels aren't answered.
	var s State
	sendRequest(&s, 4)
	qt.Check(t, qt.IsTrue(s.SentCancel(chunk(4))))
	qt.Check(t, qt.IsFalse(s.RequestCancelled(chunk(4))))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
	// It can be requested again straight away, and both chunks are accepted.
	qt.Check(t, qt.IsTrue(sendRequest(&s, 4)))
	for range 2 {
		_, _, err := s.RecvChunk(chunk(4))
		qt.Check(t, qt.IsNil(err))
	}
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(4))))
}

func TestRejectOutstanding(t *testing.T) {
	var s State
	sendRequest(&s, 5)
	outstanding, ok := s.RecvReject(chunk(5))
	qt.Check(t, qt.IsTrue(ok))
	qt.Check(t, qt.IsTrue(outstanding))
	qt.Check(t, qt.IsFalse(s.ExpectingChunk(chunk(5))))
}

func TestResendWhileCancelAwaitsReplyPanics(t *testing.T) {
	s := fastState()
	sendRequest(&s, 1)
	qt.Assert(t, qt.IsTrue(s.SentCancel(chunk(1))))
	qt.Check(t, qt.IsFalse(s.SentCancel(chunk(1))))
	qt.Check(t, qt.PanicMatches(func() { sendRequest(&s, 1) }, ".*"))
}

func peerRequest(index int) Request {
	return Request{Index: pp.Integer(index), ChunkSpec: types.ChunkSpec{Length: 16 << 10}}
}

func TestRecvRequest(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprintf("fast=%v", fast), func(t *testing.T) {
			s := NewState()
			if fast {
				s = fastState()
			}
			refused := func(action PeerRequestAction) PeerRequestAction {
				if fast {
					return action
				}
				return PeerRequestIgnore
			}
			r := peerRequest(0)
			check, action := s.RecvRequest(r, 2)
			qt.Check(t, qt.Equals(check, PeerRequestWhileChoking))
			qt.Check(t, qt.Equals(action, refused(PeerRequestReject)))
			qt.Check(t, qt.IsFalse(s.HavePeerRequest(r)))
			s.SentUnchoke()
			check, action = s.RecvRequest(r, 2)
			qt.Check(t, qt.Equals(check, PeerRequestOk))
			qt.Check(t, qt.Equals(action, PeerRequestServe))
			qt.Check(t, qt.IsTrue(s.HavePeerRequest(r)))
			check, action = s.RecvRequest(r, 2)
			qt.Check(t, qt.Equals(check, PeerRequestDuplicate))
			qt.Check(t, qt.Equals(action, refused(PeerRequestError)))
			s.RecvRequest(peerRequest(1), 2)
			check, action = s.RecvRequest(peerRequest(2), 2)
			qt.Check(t, qt.Equals(check, PeerRequestQueueFull))
			qt.Check(t, qt.Equals(action, refused(PeerRequestReject)))
			qt.Check(t, qt.Equals(s.NumPeerRequests(), 2))
		})
	}
}

func TestPeerRequestReplies(t *testing.T) {
	s := fastState()
	s.SentUnchoke()
	for i := range 3 {
		s.RecvRequest(peerRequest(i), 3)
	}
	// A Piece must match a request exactly.
	short := peerRequest(0)
	short.Length--
	qt.Check(t, qt.ErrorIs(s.SentPiece(short), ErrUnrequestedPiece))
	qt.Check(t, qt.IsNil(s.SentPiece(peerRequest(0))))
	qt.Check(t, qt.ErrorIs(s.SentPiece(peerRequest(0)), ErrUnrequestedPiece))
	qt.Check(t, qt.IsTrue(s.SentReject(peerRequest(1))))
	// Rejecting a request that was never accepted is fine.
	qt.Check(t, qt.IsFalse(s.SentReject(peerRequest(9))))
	known, sendReject := s.RecvCancel(peerRequest(2))
	qt.Check(t, qt.IsTrue(known))
	qt.Check(t, qt.IsTrue(sendReject))
	known, _ = s.RecvCancel(peerRequest(2))
	qt.Check(t, qt.IsFalse(known))
	qt.Check(t, qt.Equals(s.NumPeerRequests(), 0))
}

func TestCancelWithoutFast(t *testing.T) {
	s := NewState()
	s.SentUnchoke()
	s.RecvRequest(peerRequest(0), 1)
	known, sendReject := s.RecvCancel(peerRequest(0))
	qt.Check(t, qt.IsTrue(known))
	qt.Check(t, qt.IsFalse(sendReject))
	qt.Check(t, qt.PanicMatches(func() { s.SentReject(peerRequest(0)) }, ".*"))
}

func TestSentChoke(t *testing.T) {
	for _, fast := range []bool{false, true} {
		s := NewState()
		if fast {
			s = fastState()
		}
		changed, dropped := s.SentChoke()
		qt.Check(t, qt.IsFalse(changed))
		qt.Check(t, qt.IsFalse(dropped))
		qt.Check(t, qt.IsTrue(s.SentUnchoke()))
		qt.Check(t, qt.IsFalse(s.SentUnchoke()))
		s.RecvRequest(peerRequest(0), 1)
		changed, dropped = s.SentChoke()
		qt.Check(t, qt.IsTrue(changed))
		// BEP 3 choking discards requests. With fast, they must each be rejected or served.
		qt.Check(t, qt.Equals(dropped, !fast))
		qt.Check(t, qt.Equals(s.HavePeerRequest(peerRequest(0)), fast))
	}
}

// Entries for our requests go once there's nothing left to wait for, so the map doesn't grow with
// requests the peer has answered.
func TestOurRequestsDeletedWhenSettled(t *testing.T) {
	s := fastState()
	s.RecvUnchoke()
	for i := range 4 {
		qt.Assert(t, qt.IsTrue(sendRequest(&s, i)))
	}
	// Answered with the Piece.
	_, _, err := s.RecvChunk(chunk(0))
	qt.Assert(t, qt.IsNil(err))
	// Rejected.
	_, ok := s.RecvReject(chunk(1))
	qt.Assert(t, qt.IsTrue(ok))
	// Cancelled, then rejected.
	qt.Assert(t, qt.IsTrue(s.SentCancel(chunk(2))))
	qt.Check(t, qt.Equals(s.NumCancelled(), 1))
	_, ok = s.RecvReject(chunk(2))
	qt.Assert(t, qt.IsTrue(ok))
	// Dropped by a choke without fast: the Piece may still come.
	s.fast = false
	_, dropped := s.RecvChoke()
	qt.Check(t, qt.DeepEquals(dropped, []Request{chunk(3)}))
	qt.Check(t, qt.Equals(s.NumRequests(), 0))
	qt.Check(t, qt.HasLen(s.ourRequests, 1))
	_, _, err = s.RecvChunk(chunk(3))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(s.ourRequests, 0))
	qt.Check(t, qt.IsTrue(s.LowOnRequests()))
}

// The count of Pieces we'll accept for a chunk saturates instead of wrapping, so a peer cycling
// choke on one chunk can't make us reject the Piece for a request we just sent.
func TestExpectedChunksSaturate(t *testing.T) {
	s := NewState()
	s.RecvUnchoke()
	for range math.MaxUint16 + 10 {
		qt.Assert(t, qt.IsTrue(sendRequest(&s, 0)))
		// Without fast, a choke drops the request, and the Piece may still come.
		_, dropped := s.RecvChoke()
		qt.Assert(t, qt.HasLen(dropped, 1))
		s.RecvUnchoke()
	}
	qt.Check(t, qt.Equals(s.ourRequests[chunk(0)].expected, math.MaxUint16))
	qt.Assert(t, qt.IsTrue(sendRequest(&s, 0)))
	requested, _, err := s.RecvChunk(chunk(0))
	qt.Check(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(requested))
}
