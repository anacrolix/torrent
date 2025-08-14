package torrent

import (
	"io"

	"github.com/RoaringBitmap/roaring"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Contains implementation details that differ between peer types, like WebSeeds and regular
// BitTorrent protocol connections. These methods are embedded in the child types of Peer for legacy
// expectations that they exist on the child type. Some methods are underlined to avoid collisions
// with legacy PeerConn methods. New methods and calls that are fixed up should be migrated over to
// newHotPeerImpl.
type legacyPeerImpl interface {
	// Whether the peer should be told to update requests. Sometimes this is skipped if it's high
	// priority adjustments to requests. This is kind of only relevant to PeerConn but hasn't been
	// fully migrated over yet.
	isLowOnRequests() bool
	// Notify that the peers requests should be updated for the provided reason.
	onNeedUpdateRequests(reason updateRequestReason)

	// handleCancel initiates cancellation of a request
	handleCancel(ri RequestIndex)
	cancelAllRequests()
	connectionFlags() string
	onClose()
	onGotInfo(info *metainfo.Info)
	// Drop connection. This may be a no-op if there is no connection.
	drop()
	// Rebuke the peer
	providedBadData()
	String() string
	// Per peer-impl lines for WriteStatus.
	peerImplStatusLines() []string
	peerImplWriteStatus(w io.Writer)

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
	// Bookkeeping for a chunk being received and any specific checks.
	checkReceivedChunk(ri RequestIndex, msg *pp.Message, req Request) (intended bool, err error)
	// Whether we're expecting to receive chunks because we have outstanding requests. Used for
	// example to calculate download rate.
	expectingChunks() bool
	allConnStatsImplField(*AllConnStats) *ConnStats
}
