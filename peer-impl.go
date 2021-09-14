package torrent

import (
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types"
)

// Contains implementation details that differ between peer types, like Webseeds and regular
// BitTorrent protocol connections. Some methods are underlined so as to avoid collisions with
// legacy PeerConn methods.
type peerImpl interface {
	onNextRequestStateChanged()
	updateRequests()
	writeInterested(interested bool) bool

	// Neither of these return buffer room anymore, because they're currently both posted. There's
	// also PeerConn.writeBufferFull for when/where it matters.
	_cancel(types.Request) bool
	_request(types.Request) bool

	connectionFlags() string
	onClose()
	onGotInfo(*metainfo.Info)
	drop()
	String() string
	connStatusString() string
	writeBufferFull() bool
}
