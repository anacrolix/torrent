package torrent

import (
	"github.com/RoaringBitmap/roaring"

	"github.com/anacrolix/torrent/metainfo"
)

// Contains implementation details that differ between peer types, like Webseeds and regular
// BitTorrent protocol connections. These methods are embedded in the child types of Peer for legacy
// expectations that they exist on the child type. Some methods are underlined to avoid collisions
// with legacy PeerConn methods. New methods and calls that are fixed up should be migrated over to
// newHotPeerImpl.
type legacyPeerImpl interface {
	// Trigger the actual request state to get updated
	handleUpdateRequests()
	writeInterested(interested bool) bool

	// _cancel initiates cancellation of a request and returns acked if it expects the cancel to be
	// handled by a follow-up event.
	_cancel(RequestIndex) (acked bool)
	_request(Request) bool
	connectionFlags() string
	onClose()
	onGotInfo(*metainfo.Info)
	// Drop connection. This may be a no-op if there is no connection.
	drop()
	// Rebuke the peer
	ban()
	String() string
	peerImplStatusLines() []string

	// All if the peer should have everything, known if we know that for a fact. For example, we can
	// guess at how many pieces are in a torrent, and assume they have all pieces based on them
	// having sent haves for everything, but we don't know for sure. But if they send a have-all
	// message, then it's clear that they do.
	peerHasAllPieces() (all, known bool)
	peerPieces() *roaring.Bitmap
}

// Abstract methods implemented by subclasses of Peer.
type newHotPeerImpl interface {
	lastWriteUploadRate() float64
}
