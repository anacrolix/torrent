package peerWire

import (
	"errors"
	"iter"
	"maps"

	"github.com/anacrolix/missinggo/v2/panicif"

	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/types"
)

type RequestIndex = requestStrategy.RequestIndex

// Our requests to the peer. Each is tracked both by the wire Request we sent, which is what the
// peer's Piece, Reject and our Cancel refer to (BEP 3, BEP 6), and by the RequestIndex the request
// strategy uses. Messages are matched on the exact index, begin and length that was requested, so
// mapping between the two is never needed on receipt.

// RequestState returns our interest and outstanding requests by RequestIndex, for the request
// strategy. Don't modify it: use SentInterested, SentRequest, SentCancel, RecvChoke, RecvReject,
// RecvChunk and Closed.
func (s *State) RequestState() *requestStrategy.PeerRequestState {
	if s.requests.Requests == nil {
		s.requests.Requests = &orderedBitmap[RequestIndex]{}
	}
	return &s.requests
}

// Interested returns whether we've told the peer we're interested.
func (s *State) Interested() bool {
	return s.requests.Interested
}

// SentInterested records that we're sending Interested or NotInterested. It returns false if that
// was already the state, in which case there is nothing to send.
func (s *State) SentInterested(interested bool) (changed bool) {
	if s.requests.Interested == interested {
		return false
	}
	s.requests.Interested = interested
	return true
}

// RequestOutstanding returns whether we have requested a chunk and not had a response, or
// cancelled or deleted the request.
func (s *State) RequestOutstanding(r RequestIndex) bool {
	return s.requests.Requests != nil && s.requests.Requests.Contains(r)
}

// RequestCancelled returns whether we sent a cancel for the chunk and are waiting for the peer
// to reject it or send it anyway.
func (s *State) RequestCancelled(r RequestIndex) bool {
	return s.requests.Cancelled.Contains(r)
}

// NumRequests returns the number of outstanding requests, not counting cancelled ones.
func (s *State) NumRequests() int {
	if s.requests.Requests == nil {
		return 0
	}
	return int(s.requests.Requests.GetCardinality())
}

// LowOnRequests returns whether there's nothing outstanding with the peer, including cancels
// awaiting a response.
func (s *State) LowOnRequests() bool {
	return s.NumRequests() == 0 && s.requests.Cancelled.IsEmpty()
}

// SentRequest records that we sent req, which the request strategy knows as r. It returns false
// if the request is already outstanding, in which case there's nothing to send.
func (s *State) SentRequest(r RequestIndex, req Request) (added bool) {
	if s.RequestOutstanding(r) {
		panicif.NotEq(s.sentRequests[req], r)
		return false
	}
	// A cancelled request awaiting its reject can't be reissued: the reply would be ambiguous.
	panicif.True(s.RequestCancelled(r))
	_, ok := s.sentRequests[req]
	panicif.True(ok)
	s.RequestState().Requests.Add(r)
	if s.sentRequests == nil {
		s.sentRequests = make(map[Request]RequestIndex)
	}
	s.sentRequests[req] = r
	s.ExpectChunk(req)
	return true
}

// ExpectChunk records that a Piece answering req is acceptable, without making a request. Each
// call allows one Piece. SentRequest does this itself, so this is only for setting up tests.
func (s *State) ExpectChunk(req Request) {
	if s.validReceiveChunks == nil {
		s.validReceiveChunks = make(map[Request]int)
	}
	s.validReceiveChunks[req]++
}

// ExpectingChunk returns whether a Piece answering req would be accepted. Chunks stay acceptable
// after their request is deleted, since the peer may already have sent them.
func (s *State) ExpectingChunk(req Request) bool {
	return s.validReceiveChunks[req] > 0
}

func (s *State) decExpectedChunk(req Request) {
	count := s.validReceiveChunks[req]
	panicif.LessThanOrEqual(count, 0)
	if count == 1 {
		delete(s.validReceiveChunks, req)
	} else {
		s.validReceiveChunks[req] = count - 1
	}
}

// Removes an outstanding request, checking it was sent as req.
func (s *State) removeRequest(r RequestIndex, req Request) bool {
	if s.requests.Requests == nil || !s.requests.Requests.CheckedRemove(r) {
		return false
	}
	sent, ok := s.sentRequests[req]
	panicif.False(ok)
	panicif.NotEq(sent, r)
	delete(s.sentRequests, req)
	return true
}

// Forgets all outstanding requests without telling the peer. Pieces for them are still accepted,
// but not Rejects. Cancels awaiting a reply are kept.
func (s *State) dropRequests() (deleted []RequestIndex) {
	for req, r := range s.sentRequests {
		if !s.RequestOutstanding(r) {
			continue
		}
		panicif.False(s.requests.Requests.CheckedRemove(r))
		delete(s.sentRequests, req)
		deleted = append(deleted, r)
	}
	panicif.NotZero(s.NumRequests())
	return
}

// SentCancel records that we sent a Cancel for an outstanding request. If the peer acknowledges
// cancels with Reject or the Piece (BEP 6, see ExpectsCancelReplies), we wait for that before the
// chunk can be requested again. It returns false if the request wasn't outstanding.
func (s *State) SentCancel(r RequestIndex, req Request) bool {
	if !s.removeRequest(r, req) {
		return false
	}
	if s.ExpectsCancelReplies() {
		panicif.False(s.requests.Cancelled.CheckedAdd(r))
		s.sentRequests[req] = r
	}
	return true
}

// Looks up a request or cancel awaiting a reply by the wire request the reply names. outstanding
// is false if it was cancelled.
func (s *State) takeSentRequest(req Request) (r RequestIndex, outstanding, ok bool) {
	r, ok = s.sentRequests[req]
	if !ok {
		return
	}
	delete(s.sentRequests, req)
	if s.RequestOutstanding(r) {
		panicif.False(s.requests.Requests.CheckedRemove(r))
		outstanding = true
	} else {
		panicif.False(s.requests.Cancelled.CheckedRemove(r))
	}
	return
}

// RecvReject handles a Reject, which names the request it rejects (BEP 6). outstanding is true
// if the request hadn't been cancelled. ok is false if we have no such request or cancel, which is
// a protocol error. r is the rejected request's index.
func (s *State) RecvReject(req Request) (r RequestIndex, outstanding, ok bool) {
	r, outstanding, ok = s.takeSentRequest(req)
	if ok {
		s.decExpectedChunk(req)
	}
	return
}

var ErrUnexpectedChunk = errors.New("received unexpected chunk")

// RecvChunk handles a Piece. req is the index, begin and data length of the Piece, which must be
// exactly what was requested. requested is true if the request was outstanding, and intended is
// true if it was outstanding or cancelled; r is its index if so. A Piece we weren't expecting is
// an error (BEP 6 says to close the connection).
func (s *State) RecvChunk(req Request) (r RequestIndex, requested, intended bool, err error) {
	if !s.ExpectingChunk(req) {
		err = ErrUnexpectedChunk
		return
	}
	s.decExpectedChunk(req)
	r, requested, intended = s.takeSentRequest(req)
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

// SentPiece records that we sent the Piece for one of the peer's requests. It returns false if the
// request wasn't outstanding, for example because it was cancelled while we read the data.
func (s *State) SentPiece(r Request) (wasOutstanding bool) {
	return s.deletePeerRequest(r)
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
