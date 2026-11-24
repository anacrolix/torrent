// Package peerWire holds what can be known about a BitTorrent peer purely from the wire messages
// exchanged with it. It has no knowledge of Client or Torrent, so the rules for each message can
// be tested in isolation. Side effects on the rest of the client (piece availability, request
// updates, metrics) are left to the caller, which is told what changed.
package peerWire

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/RoaringBitmap/roaring/v2"
	g "github.com/anacrolix/generics"

	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

// State is the per-connection state derived from messages sent to and received from a peer. The
// zero value is a peer that isn't choking us and hasn't claimed any pieces, which is what
// non-BitTorrent peers like webseeds want. BitTorrent connections start with NewState.
//
// Piece counts are passed in as g.Option because a peer can send piece messages before we have
// the torrent info.
type State struct {
	// We are choking the peer.
	choking bool
	// The peer is choking us.
	peerChoking bool
	// The peer is interested in us.
	peerInterested bool

	// Pieces the peer has allowed us to request while choked (BEP 6 AllowedFast).
	peerAllowedFast typedRoaring.Bitmap[int]
	// Pieces we've told the peer we have, via Bitfield, HaveAll or Have.
	sentHaves typedRoaring.Bitmap[int]

	// The pieces the peer has claimed to have. Empty while peerSentHaveAll.
	peerPieces typedRoaring.Bitmap[int]
	// The peer has everything. This can occur due to a special message, when we may not even
	// know the number of pieces in the torrent yet.
	peerSentHaveAll bool
	// The highest possible number of pieces the torrent could have based on communication with
	// the peer. Generally only useful until we have the torrent info.
	peerMinPieces int

	// The metadata_size from the peer's extended handshake, or 0.
	peerMetadataSize int
	// Indexed by metadata piece, set to true if requested and pending a response.
	metadataRequests []bool

	// Whether the peer has sent a Port message.
	portReceived bool

	// Our interest, outstanding requests, and cancels awaiting a response. See RequestState.
	requests requestStrategy.PeerRequestState
	// The number of Piece messages we'll accept per chunk. Due to latency, buffering, and
	// implementation differences, we may receive chunks that are no longer requested. This could
	// use a roaring.BSI if the memory use becomes noticeable.
	validReceiveChunks map[Request]int
	// Our requests and cancels awaiting a Piece or Reject, by what was sent on the wire.
	sentRequests map[Request]RequestIndex

	// Requests from the peer that are outstanding.
	peerRequests map[Request]struct{}

	pex pexState

	// BEP 52 hash requests we've sent, and the hashes received for each file so far.
	sentHashRequests map[HashRequest]sentHashRequestState
	receivedHashes   map[[32]byte]*receivedFileHashes

	// BEP 55 rendezvous we've sent through this peer, awaiting Connect or Error.
	sentRendezvous map[netip.AddrPort]struct{}
}

// NewState returns the state of a BitTorrent connection before any messages: both sides are
// choking (BEP 3).
func NewState() State {
	return State{
		choking:     true,
		peerChoking: true,
	}
}

// Choking returns whether we are choking the peer.
func (s *State) Choking() bool {
	return s.choking
}

// SetChoking records that we sent Choke or Unchoke. It returns false if that's already the
// state, in which case there is nothing to send.
func (s *State) SetChoking(choking bool) (changed bool) {
	if s.choking == choking {
		return false
	}
	s.choking = choking
	return true
}

// PeerChoking returns whether the peer is choking us.
func (s *State) PeerChoking() bool {
	return s.peerChoking
}

// RecvChoke handles a Choke message. It returns false if the peer was already choking us.
func (s *State) RecvChoke() (changed bool) {
	if s.peerChoking {
		return false
	}
	s.peerChoking = true
	return true
}

// RecvUnchoke handles an Unchoke message. It returns false if the peer wasn't choking us. Some
// clients do this, and it isn't treated as an error.
func (s *State) RecvUnchoke() (changed bool) {
	if !s.peerChoking {
		return false
	}
	s.peerChoking = false
	return true
}

// PeerInterested returns whether the peer has said it is interested in us.
func (s *State) PeerInterested() bool {
	return s.peerInterested
}

// RecvInterested handles Interested and NotInterested messages. Requests from the peer aren't
// cleared on NotInterested since the spec doesn't say to. Choking them does that.
func (s *State) RecvInterested(interested bool) {
	s.peerInterested = interested
}

// PeerAllowedFast returns the pieces the peer allows us to request while choked. Don't modify it.
func (s *State) PeerAllowedFast() *typedRoaring.Bitmap[int] {
	return &s.peerAllowedFast
}

// Most pieces a peer may allow us while choked. BEP 6 doesn't bound the Allowed Fast set, but it's
// generated as a small set (libtorrent and others use around 10), and without a bound a peer
// could grow the set without limit before we know the piece count.
const MaxPeerAllowedFast = 256

// RecvAllowedFast handles an AllowedFast message (BEP 6), which "MAY list pieces that the message
// sender does not have". Pieces outside the torrent, or past MaxPeerAllowedFast, are ignored and
// added is false.
func (s *State) RecvAllowedFast(piece int, numPieces g.Option[int]) (added bool) {
	if piece < 0 || numPieces.Ok && piece >= numPieces.Value {
		return false
	}
	if s.peerAllowedFast.GetCardinality() >= MaxPeerAllowedFast {
		return false
	}
	return s.peerAllowedFast.CheckedAdd(piece)
}

// SentHave returns whether we've told the peer we have the piece.
func (s *State) SentHave(piece int) bool {
	return s.sentHaves.Contains(piece)
}

// SentHavesEmpty returns whether we've told the peer about any pieces.
func (s *State) SentHavesEmpty() bool {
	return s.sentHaves.IsEmpty()
}

// SetSentHave records that we sent Have for a piece. It returns false if the peer already knows,
// in which case there's nothing to send.
func (s *State) SetSentHave(piece int) (added bool) {
	if s.sentHaves.Contains(piece) {
		return false
	}
	s.sentHaves.Add(piece)
	return true
}

// SetSentBitfield records that we sent Bitfield with the given pieces. It must be the first
// have-related message.
func (s *State) SetSentBitfield(pieces *roaring.Bitmap) error {
	if !s.sentHaves.IsEmpty() {
		return errors.New("bitfield must be first have-related message sent")
	}
	s.sentHaves.Bitmap = *pieces.Clone()
	return nil
}

// SetSentHaveAll records that we sent HaveAll.
func (s *State) SetSentHaveAll(numPieces int) {
	s.sentHaves.Clear()
	s.sentHaves.AddRange(0, uint64(numPieces))
}

// SetSentHaveNone records that we sent HaveNone.
func (s *State) SetSentHaveNone() {
	s.sentHaves.Clear()
}

// PeerPieces returns the pieces the peer has claimed individually. It's empty if the peer sent
// HaveAll. Don't modify it.
func (s *State) PeerPieces() *typedRoaring.Bitmap[int] {
	return &s.peerPieces
}

// PeerSentHaveAll returns whether the peer's pieces are tracked as "all" rather than
// individually.
func (s *State) PeerSentHaveAll() bool {
	return s.peerSentHaveAll
}

// PeerMinPieces returns the lowest number of pieces the torrent can have, given the pieces the
// peer has claimed.
func (s *State) PeerMinPieces() int {
	return s.peerMinPieces
}

func (s *State) raisePeerMinPieces(newMin int) {
	s.peerMinPieces = max(s.peerMinPieces, newMin)
}

// PeerHasAllPieces returns whether the peer has every piece. known is false if that can't be
// determined without the number of pieces.
func (s *State) PeerHasAllPieces(numPieces g.Option[int]) (all, known bool) {
	if s.peerSentHaveAll {
		return true, true
	}
	if !numPieces.Ok {
		return false, false
	}
	return s.peerPieces.GetCardinality() == uint64(numPieces.Value), true
}

// PeerHasPiece returns whether the peer has claimed the piece.
func (s *State) PeerHasPiece(piece int, numPieces g.Option[int]) bool {
	if all, known := s.PeerHasAllPieces(numPieces); all && known {
		return true
	}
	return s.peerPieces.Contains(piece)
}

// RemotePieceCount returns how many pieces we think the peer has.
func (s *State) RemotePieceCount(numPieces g.Option[int]) int {
	if all, _ := s.PeerHasAllPieces(numPieces); all {
		return s.BestPeerNumPieces(numPieces)
	}
	return int(s.peerPieces.GetCardinality())
}

// BestPeerNumPieces returns the best guess at the number of pieces in the torrent.
func (s *State) BestPeerNumPieces(numPieces g.Option[int]) int {
	if numPieces.Ok {
		return numPieces.Value
	}
	return s.peerMinPieces
}

// SetNumPieces drops pieces claimed or allowed fast by the peer beyond the end of the torrent, once
// the info is known. Pieces dropped here were never counted towards availability, since that needs the info.
func (s *State) SetNumPieces(numPieces int) {
	s.peerPieces.RemoveRange(uint64(numPieces), roaring.MaxRange)
	s.peerAllowedFast.RemoveRange(uint64(numPieces), roaring.MaxRange)
}

// PiecesChange describes how the pieces a peer claims changed after a Bitfield, HaveAll or
// HaveNone message. While the peer has all pieces they aren't tracked individually, so moving
// in or out of that state is reported separately from Gained and Lost.
type PiecesChange struct {
	// The peer had all pieces before the message.
	WasAll bool
	// The peer has all pieces after the message.
	NowAll bool
	// Pieces that are now claimed individually and weren't claimed before.
	Gained roaring.Bitmap
	// Pieces that were claimed individually before and no longer are.
	Lost roaring.Bitmap
}

// RecvHave handles a Have message. It returns false if the peer already had the piece.
func (s *State) RecvHave(piece int, numPieces g.Option[int]) (added bool, err error) {
	if piece < 0 || numPieces.Ok && piece >= numPieces.Value {
		return false, errors.New("invalid piece")
	}
	if s.PeerHasPiece(piece, numPieces) {
		return false, nil
	}
	s.raisePeerMinPieces(piece + 1)
	s.peerPieces.Add(piece)
	return true, nil
}

// RecvBitfield handles a Bitfield message. bf is the decoded bitfield, which is a whole number of
// bytes long. Bits past the end of a torrent with known size are ignored.
func (s *State) RecvBitfield(bf []bool, numPieces g.Option[int]) (change PiecesChange, err error) {
	if len(bf)%8 != 0 {
		return change, fmt.Errorf("bitfield length %d is not a multiple of 8", len(bf))
	}
	// We know that the last byte means that at most the last 7 bits are wasted.
	s.raisePeerMinPieces(len(bf) - 7)
	if numPieces.Ok && len(bf) > numPieces.Value {
		// Ignore known excess pieces.
		bf = bf[:numPieces.Value]
	}
	var bm roaring.Bitmap
	for i, have := range bf {
		if have {
			bm.AddInt(i)
		}
	}
	if numPieces.Ok && int(bm.GetCardinality()) == numPieces.Value {
		return s.recvHaveAll(), nil
	}
	if !bm.IsEmpty() {
		s.raisePeerMinPieces(int(bm.Maximum()) + 1)
	}
	change.WasAll = s.peerSentHaveAll
	s.peerSentHaveAll = false
	change.Gained = *roaring.AndNot(&bm, &s.peerPieces.Bitmap)
	change.Lost = *roaring.AndNot(&s.peerPieces.Bitmap, &bm)
	s.peerPieces.Bitmap = bm
	return change, nil
}

// RecvHaveAll handles a HaveAll message (BEP 6).
func (s *State) RecvHaveAll() PiecesChange {
	return s.recvHaveAll()
}

func (s *State) recvHaveAll() (change PiecesChange) {
	change.WasAll = s.peerSentHaveAll
	change.NowAll = true
	change.Lost = s.peerPieces.Bitmap
	s.peerPieces = typedRoaring.Bitmap[int]{}
	s.peerSentHaveAll = true
	return
}

// RecvHaveNone handles a HaveNone message (BEP 6).
func (s *State) RecvHaveNone() (change PiecesChange) {
	change.WasAll = s.peerSentHaveAll
	change.Lost = s.peerPieces.Bitmap
	s.peerPieces = typedRoaring.Bitmap[int]{}
	s.peerSentHaveAll = false
	return
}

// PeerMetadataSize returns the metadata_size from the peer's extended handshake, or 0.
func (s *State) PeerMetadataSize() int {
	return s.peerMetadataSize
}

// SetPeerMetadataSize records the metadata_size from the peer's extended handshake. The caller
// validates it.
func (s *State) SetPeerMetadataSize(size int) {
	s.peerMetadataSize = size
}

// MetadataPieceRequested returns whether we requested a ut_metadata piece and haven't had the
// response.
func (s *State) MetadataPieceRequested(index int) bool {
	return index >= 0 && index < len(s.metadataRequests) && s.metadataRequests[index]
}

// SetMetadataPieceRequested records that we sent a ut_metadata request for a piece. It returns
// false if one is already pending.
func (s *State) SetMetadataPieceRequested(index int) (added bool) {
	if s.MetadataPieceRequested(index) {
		return false
	}
	for index >= len(s.metadataRequests) {
		s.metadataRequests = append(s.metadataRequests, false)
	}
	s.metadataRequests[index] = true
	return true
}

// RecvMetadataPiece records the response to a ut_metadata request. It returns false if we didn't
// request the piece.
func (s *State) RecvMetadataPiece(index int) (requested bool) {
	if !s.MetadataPieceRequested(index) {
		return false
	}
	s.metadataRequests[index] = false
	return true
}

// RecvPort handles a Port message. It returns true only for the first one, so a peer can't make
// us act on it repeatedly.
func (s *State) RecvPort() (first bool) {
	if s.portReceived {
		return false
	}
	s.portReceived = true
	return true
}
