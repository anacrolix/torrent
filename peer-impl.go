package torrent

import (
	"io"

	"github.com/RoaringBitmap/roaring/v2"

	"github.com/anacrolix/torrent/metainfo"
)

// A peer of a Torrent: a PeerConn (the BitTorrent protocol over TCP, uTP or WebRTC) or a
// webseedPeer. Both embed commonPeer for the state and methods they share. The unexported methods
// mean only this package implements Peer.
//
// The unexported methods are what a Torrent asks of a peer without knowing its type. They aren't
// hooks for shared code: an operation with steps that differ by peer type, like closing or
// receiving a chunk, is written on each type, and calls into commonPeer and Torrent for the steps
// they share.
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

	// The state shared by all peer types. Nothing on commonPeer calls back into the Peer that
	// embeds it: each peer type calls down into commonPeer and its Torrent.
	common() *commonPeer

	// What a Torrent asks of any of its peers.

	peerPieceClaims
	// Notify that the peer's requests should be updated for the provided reason.
	onNeedUpdateRequests(reason updateRequestReason)
	cancelAllRequests()
	onGotInfo(info *metainfo.Info)
	// Rebuke the peer
	providedBadData()
	writeStatus(w io.Writer)
	// Closes the peer and removes it from what it's counted in on its Torrent. Requires the Client
	// lock.
	close()
}

// What a peer says it has.
type peerPieceClaims interface {
	// All if the peer should have everything, known if we know that for a fact. For example, we can
	// guess at how many pieces are in a torrent, and assume they have all pieces based on them
	// having sent haves for everything, but we don't know for sure. But if they send a have-all
	// message, then it's clear that they do.
	peerHasAllPieces() (all, known bool)
	peerPieces() *roaring.Bitmap
}
