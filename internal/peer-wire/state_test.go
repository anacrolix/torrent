package peerWire

import (
	"testing"

	"github.com/RoaringBitmap/roaring/v2"
	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
)

var unknownPieces = g.None[int]()

func bitfield(numBits int, set ...int) []bool {
	bf := make([]bool, numBits)
	for _, i := range set {
		bf[i] = true
	}
	return bf
}

func bitmapOf(xs ...uint32) roaring.Bitmap {
	return *roaring.BitmapOf(xs...)
}

func checkBitmap(t *testing.T, actual *roaring.Bitmap, expected ...uint32) {
	t.Helper()
	if expected == nil {
		expected = []uint32{}
	}
	actualSlice := actual.ToArray()
	if actualSlice == nil {
		actualSlice = []uint32{}
	}
	qt.Check(t, qt.DeepEquals(actualSlice, expected))
}

func TestZeroValueIsUnchokedWithNoPieces(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.Choking()))
	qt.Check(t, qt.IsFalse(s.PeerChoking()))
	qt.Check(t, qt.IsFalse(s.PeerHasPiece(0, unknownPieces)))
	all, known := s.PeerHasAllPieces(unknownPieces)
	qt.Check(t, qt.IsFalse(all))
	qt.Check(t, qt.IsFalse(known))
}

func TestChokeAndInterest(t *testing.T) {
	s := NewState()
	qt.Check(t, qt.IsTrue(s.Choking()))
	qt.Check(t, qt.IsTrue(s.PeerChoking()))

	changed, _ := s.RecvChoke()
	qt.Check(t, qt.IsFalse(changed))
	qt.Check(t, qt.IsTrue(s.RecvUnchoke()))
	qt.Check(t, qt.IsFalse(s.PeerChoking()))
	// Redundant unchokes are tolerated, but reported as no change.
	qt.Check(t, qt.IsFalse(s.RecvUnchoke()))
	changed, _ = s.RecvChoke()
	qt.Check(t, qt.IsTrue(changed))
	qt.Check(t, qt.IsTrue(s.PeerChoking()))

	qt.Check(t, qt.IsTrue(s.SentUnchoke()))
	qt.Check(t, qt.IsFalse(s.Choking()))

	qt.Check(t, qt.IsFalse(s.PeerInterested()))
	s.RecvInterested(true)
	qt.Check(t, qt.IsTrue(s.PeerInterested()))
	s.RecvInterested(false)
	qt.Check(t, qt.IsFalse(s.PeerInterested()))
}

func TestRecvHave(t *testing.T) {
	s := NewState()
	added, err := s.RecvHave(5, unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(added))
	qt.Check(t, qt.Equals(s.PeerMinPieces(), 6))
	qt.Check(t, qt.Equals(s.BestPeerNumPieces(unknownPieces), 6))
	qt.Check(t, qt.IsTrue(s.PeerHasPiece(5, unknownPieces)))

	added, err = s.RecvHave(5, unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(added))

	_, err = s.RecvHave(-1, unknownPieces)
	qt.Check(t, qt.IsNotNil(err))
	_, err = s.RecvHave(10, g.Some(10))
	qt.Check(t, qt.IsNotNil(err))
	// A smaller Have doesn't lower the minimum.
	added, err = s.RecvHave(2, g.Some(10))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(added))
	qt.Check(t, qt.Equals(s.PeerMinPieces(), 6))
	qt.Check(t, qt.Equals(s.RemotePieceCount(g.Some(10)), 2))
}

func TestRecvHaveCompletesTorrent(t *testing.T) {
	s := NewState()
	numPieces := g.Some(2)
	for i := range 2 {
		_, err := s.RecvHave(i, numPieces)
		qt.Assert(t, qt.IsNil(err))
	}
	all, known := s.PeerHasAllPieces(numPieces)
	qt.Check(t, qt.IsTrue(all))
	qt.Check(t, qt.IsTrue(known))
	// Pieces are still tracked individually, only HaveAll or a full Bitfield switches over.
	qt.Check(t, qt.IsFalse(s.PeerSentHaveAll()))
	// Have for a piece the peer already has is redundant.
	added, err := s.RecvHave(1, numPieces)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(added))
}

func TestRecvBitfieldUnknownInfo(t *testing.T) {
	s := NewState()
	change, err := s.RecvBitfield(bitfield(16, 1, 3), unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(change.WasAll))
	qt.Check(t, qt.IsFalse(change.NowAll))
	checkBitmap(t, &change.Gained, 1, 3)
	checkBitmap(t, &change.Lost)
	// The torrent has at least 9 pieces, or the second byte wouldn't have been sent.
	qt.Check(t, qt.Equals(s.PeerMinPieces(), 9))

	// A second bitfield replaces the first.
	change, err = s.RecvBitfield(bitfield(16, 3, 12), unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	checkBitmap(t, &change.Gained, 12)
	checkBitmap(t, &change.Lost, 1)
	checkBitmap(t, &s.PeerPieces().Bitmap, 3, 12)
	qt.Check(t, qt.Equals(s.PeerMinPieces(), 13))

	// Excess bits are dropped once the size is known.
	s.GotInfo(10)
	checkBitmap(t, &s.PeerPieces().Bitmap, 3)
}

func TestRecvBitfieldIgnoresExcessBits(t *testing.T) {
	s := NewState()
	change, err := s.RecvBitfield(bitfield(8, 1, 6), g.Some(5))
	qt.Assert(t, qt.IsNil(err))
	checkBitmap(t, &change.Gained, 1)
	qt.Check(t, qt.Equals(s.RemotePieceCount(g.Some(5)), 1))
}

func TestRecvBitfieldBadLength(t *testing.T) {
	s := NewState()
	_, err := s.RecvBitfield(make([]bool, 7), unknownPieces)
	qt.Check(t, qt.IsNotNil(err))
}

func TestRecvBitfieldFullIsHaveAll(t *testing.T) {
	s := NewState()
	_, err := s.RecvHave(0, g.Some(3))
	qt.Assert(t, qt.IsNil(err))
	change, err := s.RecvBitfield(bitfield(8, 0, 1, 2), g.Some(3))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(change.WasAll))
	qt.Check(t, qt.IsTrue(change.NowAll))
	// Individually tracked pieces are handed back so their availability can be released.
	checkBitmap(t, &change.Lost, 0)
	checkBitmap(t, &change.Gained)
	qt.Check(t, qt.IsTrue(s.PeerSentHaveAll()))
	qt.Check(t, qt.IsTrue(s.PeerPieces().IsEmpty()))
}

func TestHaveAllThenBitfield(t *testing.T) {
	s := NewState()
	change := s.RecvHaveAll()
	qt.Check(t, qt.IsTrue(change.NowAll))
	qt.Check(t, qt.IsTrue(s.PeerHasPiece(1000, unknownPieces)))
	all, known := s.PeerHasAllPieces(unknownPieces)
	qt.Check(t, qt.IsTrue(all))
	qt.Check(t, qt.IsTrue(known))
	qt.Check(t, qt.Equals(s.RemotePieceCount(g.Some(4)), 4))

	change = s.RecvHaveAll()
	qt.Check(t, qt.IsTrue(change.WasAll))
	qt.Check(t, qt.IsTrue(change.NowAll))

	// Have is redundant while the peer has everything.
	added, err := s.RecvHave(2, g.Some(4))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(added))

	change, err = s.RecvBitfield(bitfield(8, 2), g.Some(4))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(change.WasAll))
	qt.Check(t, qt.IsFalse(change.NowAll))
	checkBitmap(t, &change.Gained, 2)
	checkBitmap(t, &change.Lost)
	qt.Check(t, qt.IsFalse(s.PeerSentHaveAll()))
	qt.Check(t, qt.IsFalse(s.PeerHasPiece(1, g.Some(4))))
}

func TestRecvHaveNone(t *testing.T) {
	s := NewState()
	_, err := s.RecvBitfield(bitfield(8, 4, 5), unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	change := s.RecvHaveNone()
	qt.Check(t, qt.IsFalse(change.WasAll))
	qt.Check(t, qt.IsFalse(change.NowAll))
	checkBitmap(t, &change.Lost, 4, 5)
	qt.Check(t, qt.IsTrue(s.PeerPieces().IsEmpty()))

	s.RecvHaveAll()
	change = s.RecvHaveNone()
	qt.Check(t, qt.IsTrue(change.WasAll))
	checkBitmap(t, &change.Lost)
	qt.Check(t, qt.IsFalse(s.PeerSentHaveAll()))
	all, known := s.PeerHasAllPieces(g.Some(1))
	qt.Check(t, qt.IsFalse(all))
	qt.Check(t, qt.IsTrue(known))
}

func TestPiecesChangeDoesNotAliasState(t *testing.T) {
	s := NewState()
	_, err := s.RecvBitfield(bitfield(8, 1), unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	change := s.RecvHaveAll()
	change.Lost.Add(7)
	qt.Check(t, qt.IsTrue(s.PeerPieces().IsEmpty()))
	_, err = s.RecvHave(3, unknownPieces)
	qt.Assert(t, qt.IsNil(err))
	checkBitmap(t, &change.Lost, 1, 7)
}

func TestSentHaves(t *testing.T) {
	s := NewState()
	qt.Check(t, qt.IsTrue(s.NoHavesAnnounced()))
	pieces := bitmapOf(0, 2)
	qt.Assert(t, qt.IsNil(s.SentBitfield(&pieces)))
	// The bitfield is copied.
	pieces.Add(1)
	qt.Check(t, qt.IsFalse(s.HaveAnnounced(1)))
	qt.Check(t, qt.IsTrue(s.HaveAnnounced(2)))
	qt.Check(t, qt.IsNotNil(s.SentBitfield(&pieces)))

	qt.Check(t, qt.IsTrue(s.SentHave(1)))
	qt.Check(t, qt.IsFalse(s.SentHave(1)))

	s.SentHaveNone()
	qt.Check(t, qt.IsTrue(s.NoHavesAnnounced()))
	s.SentHaveAll(3)
	qt.Check(t, qt.IsFalse(s.SentHave(2)))
	qt.Check(t, qt.IsTrue(s.SentHave(3)))
}

func TestAllowedFast(t *testing.T) {
	s := NewState()
	qt.Check(t, qt.IsTrue(s.RecvAllowedFast(3, unknownPieces)))
	qt.Check(t, qt.IsFalse(s.RecvAllowedFast(3, unknownPieces)))
	qt.Check(t, qt.IsTrue(s.RecvAllowedFast(9, unknownPieces)))
	qt.Check(t, qt.IsFalse(s.RecvAllowedFast(-1, unknownPieces)))
	qt.Check(t, qt.IsFalse(s.RecvAllowedFast(10, g.Some(10))))
	checkBitmap(t, &s.PeerAllowedFast().Bitmap, 3, 9)
	// Learning the piece count drops pieces past the end.
	s.GotInfo(5)
	checkBitmap(t, &s.PeerAllowedFast().Bitmap, 3)
}

func TestAllowedFastBounded(t *testing.T) {
	var s State
	for i := range 2 * MaxPeerAllowedFast {
		s.RecvAllowedFast(i*1000, unknownPieces)
	}
	qt.Check(t, qt.Equals(s.PeerAllowedFast().GetCardinality(), uint64(MaxPeerAllowedFast)))
}

func TestMetadataRequests(t *testing.T) {
	var s State
	qt.Check(t, qt.IsFalse(s.MetadataPieceRequested(-1)))
	qt.Check(t, qt.IsFalse(s.MetadataPieceRequested(2)))
	// A response we didn't ask for.
	qt.Check(t, qt.IsFalse(s.RecvMetadataPiece(2)))
	qt.Check(t, qt.IsTrue(s.SentMetadataRequest(2)))
	qt.Check(t, qt.IsFalse(s.SentMetadataRequest(2)))
	qt.Check(t, qt.IsTrue(s.MetadataPieceRequested(2)))
	qt.Check(t, qt.IsFalse(s.MetadataPieceRequested(1)))
	qt.Check(t, qt.IsTrue(s.RecvMetadataPiece(2)))
	// Only one response per request.
	qt.Check(t, qt.IsFalse(s.RecvMetadataPiece(2)))
	qt.Check(t, qt.IsTrue(s.SentMetadataRequest(2)))
}

func TestOnlyFirstPortIsActedOn(t *testing.T) {
	var s State
	qt.Check(t, qt.IsTrue(s.RecvPort()))
	qt.Check(t, qt.IsFalse(s.RecvPort()))
}
