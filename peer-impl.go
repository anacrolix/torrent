package torrent

import (
	"github.com/RoaringBitmap/roaring"

	"github.com/anacrolix/torrent/metainfo"
)

// Contains implementation details that differ between peer types, like Webseeds and regular
// BitTorrent protocol connections. Some methods are underlined so as to avoid collisions with
// legacy PeerConn methods.
type peerImpl interface {
	// Trigger the actual request state to get updated
	handleUpdateRequests(lock bool, lockTorrent bool)
	writeInterested(interested bool, lock bool) bool

	// _cancel initiates cancellation of a request and returns acked if it expects the cancel to be
	// handled by a follow-up event.
	_cancel(r RequestIndex, lock bool, lockTorrent bool) (acked bool)
	_request(r Request, lock bool) bool
	connectionFlags() string
	onClose(lockTorrent bool)
	onGotInfo(info *metainfo.Info, lock bool, lockTorrent bool)
	// Drop connection. This may be a no-op if there is no connection.
	drop(lockTorrent bool)
	// Rebuke the peer
	ban()
	String() string
	peerImplStatusLines(lock bool) []string

	// the peers is running low on requests and needs some more
	// Note: for web peers this means low on web requests - as we
	// want to drive the http request api while we are processing
	// its responses
	isLowOnRequests(lock bool, lockTorrent bool) bool

	// All if the peer should have everything, known if we know that for a fact. For example, we can
	// guess at how many pieces are in a torrent, and assume they have all pieces based on them
	// having sent haves for everything, but we don't know for sure. But if they send a have-all
	// message, then it's clear that they do.
	peerHasAllPieces(lock bool, lockTorrent bool) (all, known bool)
	peerPieces(lock bool) *roaring.Bitmap

	nominalMaxRequests(lock bool, lockTorrent bool) maxRequests
}
