package peerWire

import (
	"errors"
	"iter"
	"maps"

	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/types"
)

// Our requests to the peer, as the wire Requests we sent. The peer's Piece and Reject, and our
// Cancel, refer to a request by its index, begin and length (BEP 3, BEP 6), and they're matched
// exactly. Mapping requests to the caller's own indexes is left to the caller.

// Interested returns whether we've told the peer we're interested.
func (s *State) Interested() bool {
	return s.interested
}

// SentInterested records that we're sending Interested or NotInterested. It returns false if that
// was already the state, in which case there is nothing to send.
func (s *State) SentInterested(interested bool) (changed bool) {
	if s.interested == interested {
		return false
	}
	s.interested = interested
	return true
}

// What we're waiting for from the peer about one chunk. Entries with nothing to wait for are
// deleted, so the map stays the size of what's in flight.
type ourRequest struct {
	// Sent and not yet answered, cancelled or dropped.
	outstanding bool
	// We sent a cancel and await a Piece or Reject. See ExpectsCancelReplies.
	cancelled bool
	// The number of Piece messages we'll accept. Due to latency, buffering, and implementation
	// differences, we may receive chunks that are no longer requested. Every outstanding or
	// cancelled request counts one.
	expected uint32
}

func (r ourRequest) empty() bool {
	return !r.outstanding && !r.cancelled && r.expected == 0
}

// Stores r for req, deleting the entry if there's nothing left to wait for.
func (s *State) putOurRequest(req Request, r ourRequest) {
	if r.empty() {
		delete(s.ourRequests, req)
		return
	}
	if s.ourRequests == nil {
		s.ourRequests = make(map[Request]ourRequest)
	}
	s.ourRequests[req] = r
}

// RequestOutstanding returns whether we have requested a chunk and not had a response, or
// cancelled the request, or had it dropped.
func (s *State) RequestOutstanding(req Request) bool {
	return s.ourRequests[req].outstanding
}

// RequestCancelled returns whether we sent a cancel for the request and are waiting for the peer
// to reject it or send it anyway.
func (s *State) RequestCancelled(req Request) bool {
	return s.ourRequests[req].cancelled
}

// NumRequests returns the number of outstanding requests, not counting cancelled ones.
func (s *State) NumRequests() int {
	return s.numRequests
}

// NumCancelled returns the number of cancels awaiting a reply.
func (s *State) NumCancelled() int {
	return s.numCancelled
}

// LowOnRequests returns whether there's nothing outstanding with the peer, including cancels
// awaiting a response.
func (s *State) LowOnRequests() bool {
	return s.NumRequests() == 0 && s.NumCancelled() == 0
}

// SentRequest records that we're sending req. It returns false if it's already outstanding, in
// which case there's nothing to send. A request can't be sent again while a cancel for it awaits
// a reply, since the reply would be ambiguous.
func (s *State) SentRequest(req Request) (added bool) {
	r := s.ourRequests[req]
	if r.outstanding {
		return false
	}
	panicif.True(r.cancelled)
	r.outstanding = true
	r.expected++
	s.numRequests++
	s.putOurRequest(req, r)
	return true
}

// ExpectChunk records that a Piece answering req is acceptable, without making a request. Each
// call allows one Piece. SentRequest does this itself, so this is only for setting up tests.
func (s *State) ExpectChunk(req Request) {
	r := s.ourRequests[req]
	r.expected++
	s.putOurRequest(req, r)
}

// ExpectingChunk returns whether a Piece answering req would be accepted. Chunks stay acceptable
// after their request is dropped, since the peer may already have sent them.
func (s *State) ExpectingChunk(req Request) bool {
	return s.ourRequests[req].expected > 0
}

// Forgets all outstanding requests without telling the peer. Pieces for them are still accepted,
// but not Rejects. Cancels awaiting a reply are kept.
func (s *State) dropRequests() (dropped []Request) {
	if s.numRequests == 0 {
		return
	}
	for req, r := range s.ourRequests {
		if !r.outstanding {
			continue
		}
		dropped = append(dropped, req)
		r.outstanding = false
		// expected is at least 1, so the entry stays and this doesn't delete during iteration.
		s.ourRequests[req] = r
	}
	s.numRequests = 0
	return
}

// SentCancel records that we're sending a Cancel for an outstanding request. If the peer
// acknowledges cancels with Reject or the Piece (BEP 6, see ExpectsCancelReplies), we wait for
// that before the chunk can be requested again. It returns false if the request wasn't
// outstanding, in which case there's nothing to send.
func (s *State) SentCancel(req Request) bool {
	r := s.ourRequests[req]
	if !r.outstanding {
		return false
	}
	r.outstanding = false
	s.numRequests--
	if s.ExpectsCancelReplies() {
		r.cancelled = true
		s.numCancelled++
	}
	s.putOurRequest(req, r)
	return true
}

// Takes the request or cancel awaiting the reply naming req from r. outstanding is false if it
// was cancelled.
func (s *State) takeAwaitingReply(r *ourRequest) (outstanding, ok bool) {
	switch {
	case r.outstanding:
		r.outstanding = false
		s.numRequests--
		return true, true
	case r.cancelled:
		r.cancelled = false
		s.numCancelled--
		return false, true
	}
	return false, false
}

// RecvReject handles a Reject, which names the request it rejects (BEP 6). outstanding is true if
// the request hadn't been cancelled. ok is false if we have no such request or cancel, which is a
// protocol error.
func (s *State) RecvReject(req Request) (outstanding, ok bool) {
	r := s.ourRequests[req]
	outstanding, ok = s.takeAwaitingReply(&r)
	if !ok {
		return
	}
	panicif.Zero(r.expected)
	r.expected--
	s.putOurRequest(req, r)
	return
}

var ErrUnexpectedChunk = errors.New("received unexpected chunk")

// RecvChunk handles a Piece. req is the index, begin and data length of the Piece, which must be
// exactly what was requested. requested is true if the request was outstanding, and intended is
// true if it was outstanding or cancelled. A Piece we weren't expecting is an error (BEP 6 says to
// close the connection).
func (s *State) RecvChunk(req Request) (requested, intended bool, err error) {
	r := s.ourRequests[req]
	if r.expected == 0 {
		err = ErrUnexpectedChunk
		return
	}
	r.expected--
	requested, intended = s.takeAwaitingReply(&r)
	s.putOurRequest(req, r)
	return
}

// Requests from the peer to us. They're outstanding until served, rejected, cancelled or
// dropped by choking. Reading and buffering the data is left to the caller.

// Request is a request for a chunk, as sent over the wire.
type Request = types.Request

// PeerRequestCheck is the protocol rule a Request from the peer met. See RecvRequest.
type PeerRequestCheck int

const (
	// The request is accepted, subject to the caller's own checks.
	PeerRequestOk PeerRequestCheck = iota
	// The request is already outstanding. With the fast extension that's a protocol error,
	// otherwise it's ignored.
	PeerRequestDuplicate
	// We're choking the peer. It's rejected with the fast extension, otherwise ignored.
	PeerRequestWhileChoking
	// Too many requests are outstanding. It's rejected with the fast extension, otherwise
	// ignored. BEP 6 allows closing the connection.
	PeerRequestQueueFull
)

// PeerRequestAction is what to do about a Request from the peer.
type PeerRequestAction int

const (
	// The request is outstanding. Serve it, or send a Reject and call SentReject.
	PeerRequestServe PeerRequestAction = iota
	// Do nothing.
	PeerRequestIgnore
	// Send a Reject. The request was never outstanding.
	PeerRequestReject
	// Close the connection.
	PeerRequestError
)

var ErrDuplicateRequest = errors.New("received duplicate request with fast enabled")

// RecvRequest handles a Request from the peer, applying the protocol rules in order: duplicates,
// then choking, then at most maxOutstanding outstanding. If the request is accepted it's
// outstanding until SentPiece, SentReject, RecvCancel, SentChoke or Closed.
func (s *State) RecvRequest(r Request, maxOutstanding int) (check PeerRequestCheck, action PeerRequestAction) {
	refuse := func(c PeerRequestCheck, fastAction PeerRequestAction) (PeerRequestCheck, PeerRequestAction) {
		if s.fast {
			return c, fastAction
		}
		return c, PeerRequestIgnore
	}
	switch {
	case s.HavePeerRequest(r):
		return refuse(PeerRequestDuplicate, PeerRequestError)
	case s.choking:
		return refuse(PeerRequestWhileChoking, PeerRequestReject)
	case s.NumPeerRequests() >= maxOutstanding:
		return refuse(PeerRequestQueueFull, PeerRequestReject)
	}
	if s.peerRequests == nil {
		s.peerRequests = make(map[Request]struct{})
	}
	s.peerRequests[r] = struct{}{}
	return PeerRequestOk, PeerRequestServe
}

// HavePeerRequest returns whether the peer's request is outstanding.
func (s *State) HavePeerRequest(r Request) bool {
	_, ok := s.peerRequests[r]
	return ok
}

// NumPeerRequests returns how many of the peer's requests are outstanding.
func (s *State) NumPeerRequests() int {
	return len(s.peerRequests)
}

// PeerRequests iterates the peer's outstanding requests, in no particular order. Don't modify
// the state while iterating.
func (s *State) PeerRequests() iter.Seq[Request] {
	return maps.Keys(s.peerRequests)
}

var ErrUnrequestedPiece = errors.New("piece doesn't match an outstanding request from the peer")

// SentPiece records that we're sending a Piece. piece is its index, begin and data length, which
// must be exactly one of the peer's outstanding requests, which it then answers. Otherwise it's an
// error and the Piece mustn't be sent.
func (s *State) SentPiece(piece Request) error {
	if !s.deletePeerRequest(piece) {
		return ErrUnrequestedPiece
	}
	return nil
}

// SentReject records that we sent a Reject (BEP 6), which needs the fast extension. It returns
// whether the request was outstanding: a request can be rejected before it's accepted.
func (s *State) SentReject(r Request) (wasOutstanding bool) {
	panicif.False(s.fast)
	return s.deletePeerRequest(r)
}

// RecvCancel handles a Cancel from the peer. known is false if the request wasn't outstanding. It
// isn't outstanding after this either way. With the fast extension every request gets exactly one
// reply (BEP 6), so sendReject says to answer the cancel with a Reject.
func (s *State) RecvCancel(r Request) (known, sendReject bool) {
	if !s.deletePeerRequest(r) {
		return false, false
	}
	return true, s.fast
}

func (s *State) deletePeerRequest(r Request) bool {
	if !s.HavePeerRequest(r) {
		return false
	}
	delete(s.peerRequests, r)
	return true
}
