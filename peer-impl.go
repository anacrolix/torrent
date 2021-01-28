package torrent

import (
	"github.com/anacrolix/torrent/metainfo"
)

// Contains implementation details that differ between peer types, like Webseeds and regular
// BitTorrent protocol connections. Some methods are underlined so as to avoid collisions with
// legacy PeerConn methods.
type peerImpl interface {
	updateRequests()
	writeInterested(interested bool) bool
	cancel(Request) bool
	// Return true if there's room for more activity.
	request(Request) bool
	connectionFlags() string
	onClose()
	_postCancel(Request)
	onGotInfo(*metainfo.Info)
	drop()
	String() string
	connStatusString() string
}
