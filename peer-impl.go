package torrent

import (
	"io"
	"net"

	"github.com/RoaringBitmap/roaring/v2"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// A peer of a Torrent: a PeerConn (the BitTorrent protocol over TCP, uTP or WebRTC) or a
// webseedPeer. Both embed commonPeer for the state and methods they share. The unexported methods
// mean only this package implements Peer.
type Peer interface {
	// Returns the Torrent a Peer belongs to. Shouldn't change for the lifetime of the Peer. May be
	// nil if we are the receiving end of a connection and the handshake hasn't been received or
	// accepted yet.
	Torrent() *Torrent
	Stats() PeerStats
	DownloadRate() float64
	Close() error
	String() string
	// Returns the PeerConn if this Peer is one. Equivalent to a type assertion.
	TryAsPeerConn() (*PeerConn, bool)

	// The state shared by all peer types.
	common() *commonPeer

	// Shared, from commonPeer.

	remoteIp() net.IP
	trust() connectionTrust

	// Requests.

	// Whether the peer should be told to update requests. Sometimes this is skipped if it's high
	// priority adjustments to requests. Webseeds always return false, as their requests are
	// updated across the Client.
	isLowOnRequests() bool
	// Notify that the peer's requests should be updated for the provided reason.
	onNeedUpdateRequests(reason updateRequestReason)
	cancelAllRequests()
	// Bookkeeping for a chunk being received and any specific checks.
	checkReceivedChunk(ri RequestIndex, msg *pp.Message, req Request) (intended bool, err error)
	// Whether we're expecting to receive chunks because we have outstanding requests. Used for
	// example to calculate download rate.
	expectingChunks() bool

	// What the peer has.

	// All if the peer should have everything, known if we know that for a fact. For example, we can
	// guess at how many pieces are in a torrent, and assume they have all pieces based on them
	// having sent haves for everything, but we don't know for sure. But if they send a have-all
	// message, then it's clear that they do.
	peerHasAllPieces() (all, known bool)
	peerPieces() *roaring.Bitmap
	// The fewest pieces the torrent can have given what the peer has claimed. Used to guess the
	// piece count before we have the info.
	peerMinPieces() pieceIndex

	// Lifecycle.

	onGotInfo(info *metainfo.Info)
	onClose()
	// Drop connection. This may be a no-op if there is no connection.
	drop()
	// Rebuke the peer
	providedBadData()

	// Stats and status.

	lastWriteUploadRate() float64
	allConnStatsImplField(*AllConnStats) *ConnStats
	// Per peer-impl lines for WriteStatus.
	peerImplStatusLines() []string
	peerImplWriteStatus(w io.Writer)
}

// The exported Peer methods that depend on the peer type, defined on each type.

func (cn *PeerConn) Stats() PeerStats { return peerStats(cn) }

func (cn *PeerConn) Close() error { return closePeerLocked(cn) }

func (cn *PeerConn) TryAsPeerConn() (*PeerConn, bool) { return cn, true }

func (ws *webseedPeer) Stats() PeerStats { return peerStats(ws) }

func (ws *webseedPeer) Close() error { return closePeerLocked(ws) }

func (ws *webseedPeer) TryAsPeerConn() (*PeerConn, bool) { return nil, false }
