package peerWire

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/bits"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// BEP 52 hash requests. A hash request, its Hashes reply and a Hash Reject all carry the same
// fields, and the reply is correlated with the request by them.

// HashRequest is the payload of a BEP 52 hash request, hashes or hash reject message.
type HashRequest struct {
	PiecesRoot                            [32]byte
	BaseLayer, Index, Length, ProofLayers pp.Integer
}

func HashRequestFromMessage(m *pp.Message) HashRequest {
	return HashRequest{
		PiecesRoot:  m.PiecesRoot,
		BaseLayer:   m.BaseLayer,
		Index:       m.Index,
		Length:      m.Length,
		ProofLayers: m.ProofLayers,
	}
}

// Message returns the message of type t with the request's payload.
func (hr HashRequest) Message(t pp.MessageType) pp.Message {
	return pp.Message{
		Type:        t,
		PiecesRoot:  hr.PiecesRoot,
		BaseLayer:   hr.BaseLayer,
		Index:       hr.Index,
		Length:      hr.Length,
		ProofLayers: hr.ProofLayers,
	}
}

// BEP 52: "Length SHOULD NOT be greater than 512."
const MaxHashRequestLength = 512

// CheckHashRequest applies the BEP 52 rules to a hash request: "Length MUST be
// equal-to-or-greater-than two and a power of two", "Index MUST be a multiple of length", and length
// SHOULD NOT exceed 512. We only serve baseLayer, the piece layer, and no proof layers. An error
// means the request should be rejected with Hash Reject.
func CheckHashRequest(hr HashRequest, baseLayer pp.Integer) error {
	if hr.Length < 2 || bits.OnesCount64(uint64(hr.Length)) != 1 {
		return fmt.Errorf("length %d is not a power of two of at least 2", hr.Length)
	}
	if hr.Length > MaxHashRequestLength {
		return fmt.Errorf("length %d exceeds %d", hr.Length, MaxHashRequestLength)
	}
	if hr.Index%hr.Length != 0 {
		return fmt.Errorf("index %d is not a multiple of length %d", hr.Index, hr.Length)
	}
	if hr.BaseLayer != baseLayer {
		return fmt.Errorf("base layer %d isn't the piece layer %d", hr.BaseLayer, baseLayer)
	}
	if hr.ProofLayers != 0 {
		return errors.New("proof layers not supported")
	}
	return nil
}

type sentHashRequestState int

const (
	hashRequestOutstanding sentHashRequestState = iota
	// Answered with Hashes. Kept so we don't request it again while the rest of the file's hashes
	// arrive.
	hashRequestAnswered
	// Answered with Hash Reject. Kept so we don't keep asking a peer that won't serve it.
	hashRequestRejected
)

// Hashes received from the peer for one file, until all have arrived.
type receivedFileHashes struct {
	hashes      [][32]byte
	received    []bool
	numReceived int
}

// Adds hashes starting at file piece index. Hashes past the end of the file are padding.
func (me *receivedFileHashes) add(index int, hashes [][32]byte) {
	for i, h := range hashes {
		j := index + i
		if j >= len(me.hashes) {
			break
		}
		me.hashes[j] = h
		if !me.received[j] {
			me.received[j] = true
			me.numReceived++
		}
	}
}

// SentHashRequest records that we sent hr. It returns false if it was sent before, whether or not
// it has been answered, in which case it shouldn't be sent again.
func (s *State) SentHashRequest(hr HashRequest) (added bool) {
	s.event()
	if _, ok := s.sentHashRequests[hr]; ok {
		return false
	}
	if s.sentHashRequests == nil {
		s.sentHashRequests = make(map[HashRequest]sentHashRequestState)
	}
	s.sentHashRequests[hr] = hashRequestOutstanding
	return true
}

// HashRequestSent returns whether we've sent hr, answered or not.
func (s *State) HashRequestSent(hr HashRequest) bool {
	_, ok := s.sentHashRequests[hr]
	return ok
}

// OutstandingHashRequests iterates our hash requests awaiting a reply. Don't modify the state
// while iterating.
func (s *State) OutstandingHashRequests() iter.Seq[HashRequest] {
	return func(yield func(HashRequest) bool) {
		for hr, st := range maps.All(s.sentHashRequests) {
			if st == hashRequestOutstanding && !yield(hr) {
				return
			}
		}
	}
}

// Marks an outstanding hash request answered with st.
func (s *State) answerHashRequest(hr HashRequest, st sentHashRequestState) error {
	cur, ok := s.sentHashRequests[hr]
	if !ok || cur != hashRequestOutstanding {
		// BEP 52: a hashes message "MUST correlate with a 'hash request' message".
		return fmt.Errorf("unsolicited reply to hash request %v", hr)
	}
	s.sentHashRequests[hr] = st
	return nil
}

// RecvHashReject handles a Hash Reject. It's an error if it doesn't answer an outstanding request.
// The request isn't made again.
func (s *State) RecvHashReject(hr HashRequest) error {
	s.event()
	return s.answerHashRequest(hr, hashRequestRejected)
}

// RecvHashes handles a Hashes message answering hr, for a file with numPieces pieces, which the
// caller looks up by the pieces root. Once every piece hash for the file has arrived, they're
// returned for the caller to verify against the pieces root, and forgotten here.
func (s *State) RecvHashes(
	hr HashRequest,
	hashes [][32]byte,
	numPieces int,
) (complete [][32]byte, err error) {
	s.event()
	if err = s.answerHashRequest(hr, hashRequestAnswered); err != nil {
		return
	}
	if hr.ProofLayers != 0 {
		err = errors.New("proof layers not supported")
		return
	}
	if uint64(hr.Length) != uint64(len(hashes)) {
		err = fmt.Errorf("received %d hashes for length %d", len(hashes), hr.Length)
		return
	}
	if uint64(hr.Index) > uint64(numPieces) {
		err = fmt.Errorf("hash index %d is past file piece count %d", hr.Index, numPieces)
		return
	}
	fileHashes := s.receivedHashes[hr.PiecesRoot]
	if fileHashes == nil {
		fileHashes = &receivedFileHashes{
			hashes:   make([][32]byte, numPieces),
			received: make([]bool, numPieces),
		}
		if s.receivedHashes == nil {
			s.receivedHashes = make(map[[32]byte]*receivedFileHashes)
		}
		s.receivedHashes[hr.PiecesRoot] = fileHashes
	}
	fileHashes.add(int(hr.Index), hashes)
	if fileHashes.numReceived < numPieces {
		return
	}
	delete(s.receivedHashes, hr.PiecesRoot)
	complete = fileHashes.hashes
	return
}

// NumFilesReceivingHashes returns the number of files we have some but not all hashes for.
func (s *State) NumFilesReceivingHashes() int {
	return len(s.receivedHashes)
}
