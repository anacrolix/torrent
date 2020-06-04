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
	cancel(request) bool
	// Return true if there's room for more activity.
	request(request) bool
	connectionFlags() string
	_close()
	_postCancel(request)
	onGotInfo(*metainfo.Info)
	drop()
	String() string
}
