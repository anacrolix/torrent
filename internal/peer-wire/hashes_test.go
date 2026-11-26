package peerWire

import (
	"slices"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestCheckHashRequest(t *testing.T) {
	const pieceLayer = 2
	ok := HashRequest{BaseLayer: pieceLayer, Index: 512, Length: 512}
	qt.Check(t, qt.IsNil(CheckHashRequest(ok, pieceLayer)))
	for _, hr := range []HashRequest{
		{BaseLayer: pieceLayer, Length: 1},
		{BaseLayer: pieceLayer, Length: 6},
		{BaseLayer: pieceLayer, Length: 1024},
		{BaseLayer: pieceLayer, Index: 2, Length: 4},
		{BaseLayer: 0, Length: 2},
		{BaseLayer: pieceLayer, Length: 2, ProofLayers: 1},
	} {
		qt.Check(t, qt.IsNotNil(CheckHashRequest(hr, pieceLayer)), qt.Commentf("%+v", hr))
	}
}

func TestHashRequestsAnswered(t *testing.T) {
	var s State
	a := HashRequest{PiecesRoot: [32]byte{1}, Index: 0, Length: 2}
	b := HashRequest{PiecesRoot: [32]byte{1}, Index: 2, Length: 2}
	qt.Assert(t, qt.IsTrue(s.SentHashRequest(a)))
	qt.Assert(t, qt.IsFalse(s.SentHashRequest(a)))
	qt.Assert(t, qt.IsTrue(s.SentHashRequest(b)))
	// Unsolicited replies.
	_, err := s.RecvHashes(HashRequest{Length: 2}, make([][32]byte, 2), 4)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNotNil(s.RecvHashReject(HashRequest{Length: 2})))

	hashes, err := s.RecvHashes(a, [][32]byte{{1}, {2}}, 3)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsNil(hashes))
	qt.Check(t, qt.Equals(s.NumFilesReceivingHashes(), 1))
	// Answered, but not made again while the rest of the file arrives.
	qt.Check(t, qt.IsFalse(s.SentHashRequest(a)))
	qt.Check(t, qt.DeepEquals(slices.Collect(s.OutstandingHashRequests()), []HashRequest{b}))
	// One reply per request.
	_, err = s.RecvHashes(a, [][32]byte{{1}, {2}}, 3)
	qt.Check(t, qt.IsNotNil(err))
	// The second hash past the end of the file is padding.
	hashes, err = s.RecvHashes(b, [][32]byte{{3}, {}}, 3)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(hashes, [][32]byte{{1}, {2}, {3}}))
	qt.Check(t, qt.Equals(s.NumFilesReceivingHashes(), 0))
}

func TestHashesWrongLength(t *testing.T) {
	var s State
	a := HashRequest{Length: 2}
	s.SentHashRequest(a)
	_, err := s.RecvHashes(a, make([][32]byte, 1), 2)
	qt.Check(t, qt.IsNotNil(err))
}

func TestHashReject(t *testing.T) {
	var s State
	a := HashRequest{Length: 2}
	s.SentHashRequest(a)
	qt.Assert(t, qt.IsNil(s.RecvHashReject(a)))
	qt.Check(t, qt.IsNotNil(s.RecvHashReject(a)))
	_, err := s.RecvHashes(a, make([][32]byte, 2), 2)
	qt.Check(t, qt.IsNotNil(err))
	// Not asked again.
	qt.Check(t, qt.IsFalse(s.SentHashRequest(a)))
}
