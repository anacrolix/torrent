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
// strategy. Don't modify it: use SetInterested, SentRequest, DeleteRequest, SentCancel,
// RecvReject and RecvChunk.
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

// SetInterested records that we sent Interested or NotInterested. It returns false if that was
// already the state, in which case there is nothing to send.
func (s *State) SetInterested(interested bool) (changed bool) {
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
// call allows one Piece. SentRequest does this itself.
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

// DeleteRequest forgets an outstanding request without telling the peer, for example when a
// peer without the fast extension chokes us. A Piece for it is still accepted, but not a Reject.
// It returns false if the request wasn't outstanding.
func (s *State) DeleteRequest(r RequestIndex, req Request) bool {
	return s.removeRequest(r, req)
}

// SentCancel records that we sent a Cancel for an outstanding request. If expectReject, the
// peer acknowledges cancels with Reject or the Piece (BEP 6), and we wait for that before the
// chunk can be requested again. It returns false if the request wasn't outstanding.
func (s *State) SentCancel(r RequestIndex, req Request, expectReject bool) bool {
	if !s.removeRequest(r, req) {
		return false
	}
	if expectReject {
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

// PeerRequestCheck is the outcome of checking a Request from the peer against the protocol
// rules that don't depend on the torrent.
type PeerRequestCheck int

const (
	// The request can be served, subject to the caller's own checks.
	PeerRequestOk PeerRequestCheck = iota
	// The request is already outstanding. With the fast extension that's a protocol error,
	// otherwise it's ignored.
	PeerRequestDuplicate
	// We're choking the peer. Reject it with the fast extension, otherwise ignore it.
	PeerRequestWhileChoking
	// Too many requests are outstanding. Reject it with the fast extension, otherwise ignore
	// it. BEP 6 allows closing the connection.
	PeerRequestQueueFull
)

// CheckPeerRequest applies the protocol rules for a Request from the peer, in order. It doesn't
// change the state: call AddPeerRequest if the request is accepted.
func (s *State) CheckPeerRequest(r Request, maxOutstanding int) PeerRequestCheck {
	if s.HavePeerRequest(r) {
		return PeerRequestDuplicate
	}
	if s.choking {
		return PeerRequestWhileChoking
	}
	if s.NumPeerRequests() >= maxOutstanding {
		return PeerRequestQueueFull
	}
	return PeerRequestOk
}

// AddPeerRequest records an accepted Request from the peer.
func (s *State) AddPeerRequest(r Request) {
	if s.peerRequests == nil {
		s.peerRequests = make(map[Request]struct{})
	}
	s.peerRequests[r] = struct{}{}
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

// DeletePeerRequest records that the peer's request was served, rejected or cancelled. It
// returns false if it wasn't outstanding.
func (s *State) DeletePeerRequest(r Request) bool {
	if !s.HavePeerRequest(r) {
		return false
	}
	delete(s.peerRequests, r)
	return true
}

// ClearPeerRequests forgets all the peer's requests, as happens when choking a peer without the
// fast extension.
func (s *State) ClearPeerRequests() {
	clear(s.peerRequests)
}
