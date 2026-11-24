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

// Our requests to the peer. Requests are identified by index in the torrent's chunks; mapping
// to and from wire messages is left to the caller, which knows the chunk layout.

// RequestState returns our interest and outstanding requests, for the request strategy. Don't
// modify it: use SetInterested, SentRequest, DeleteRequest, SentCancel, RecvReject and RecvChunk.
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

// SentRequest records that we sent a Request for the chunk. It returns false if the request is
// already outstanding, in which case there's nothing to send.
func (s *State) SentRequest(r RequestIndex) (added bool) {
	if s.RequestOutstanding(r) {
		return false
	}
	s.RequestState().Requests.Add(r)
	s.ExpectChunk(r)
	return true
}

// ExpectChunk records that a Piece for the chunk is acceptable, without making a request. Each
// call allows one Piece. SentRequest does this itself.
func (s *State) ExpectChunk(r RequestIndex) {
	if s.validReceiveChunks == nil {
		s.validReceiveChunks = make(map[RequestIndex]int)
	}
	s.validReceiveChunks[r]++
}

// ExpectingChunk returns whether a Piece for the chunk would be accepted. Chunks stay acceptable
// after their request is deleted, since the peer may already have sent them.
func (s *State) ExpectingChunk(r RequestIndex) bool {
	return s.validReceiveChunks[r] > 0
}

func (s *State) decExpectedChunk(r RequestIndex) {
	count := s.validReceiveChunks[r]
	panicif.LessThanOrEqual(count, 0)
	if count == 1 {
		delete(s.validReceiveChunks, r)
	} else {
		s.validReceiveChunks[r] = count - 1
	}
}

// DeleteRequest forgets an outstanding request without telling the peer, for example when a
// peer without the fast extension chokes us. A Piece for it is still accepted. It returns false
// if the request wasn't outstanding.
func (s *State) DeleteRequest(r RequestIndex) bool {
	return s.requests.Requests != nil && s.requests.Requests.CheckedRemove(r)
}

// SentCancel records that we sent a Cancel for an outstanding request. If expectReject, the
// peer acknowledges cancels with Reject (BEP 6), and we wait for that or the chunk before the
// chunk can be requested again. It returns false if the request wasn't outstanding.
func (s *State) SentCancel(r RequestIndex, expectReject bool) bool {
	if !s.DeleteRequest(r) {
		return false
	}
	if expectReject {
		panicif.False(s.requests.Cancelled.CheckedAdd(r))
	}
	return true
}

// RecvReject handles a Reject for one of our requests. outstanding is true if the request hadn't
// been cancelled. ok is false if we have no such request or cancel, which is a protocol error.
func (s *State) RecvReject(r RequestIndex) (outstanding, ok bool) {
	if s.DeleteRequest(r) {
		outstanding = true
	} else if !s.requests.Cancelled.CheckedRemove(r) {
		return false, false
	}
	s.decExpectedChunk(r)
	return outstanding, true
}

var ErrUnexpectedChunk = errors.New("received unexpected chunk")

// RecvChunk handles a Piece. requested is true if the request was outstanding, and intended is
// true if it was outstanding or cancelled. A Piece we weren't expecting is an error.
func (s *State) RecvChunk(r RequestIndex) (requested, intended bool, err error) {
	if !s.ExpectingChunk(r) {
		err = ErrUnexpectedChunk
		return
	}
	s.decExpectedChunk(r)
	requested = s.DeleteRequest(r)
	intended = requested || s.requests.Cancelled.CheckedRemove(r)
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
