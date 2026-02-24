package torrent

import (
	"net/url"
)

// A handle to announcer status and actions for a Torrent.
type torrentRegularTrackerAnnouncer struct {
	u                *url.URL
	getAnnounceState func() announceState
}

var _ torrentTrackerAnnouncer = torrentRegularTrackerAnnouncer{}

func (r torrentRegularTrackerAnnouncer) statusLine() string {
	return regularTrackerScraperStatusLine(r.getAnnounceState())
}

func (r torrentRegularTrackerAnnouncer) URL() *url.URL {
	return r.u
}

func (r torrentRegularTrackerAnnouncer) Stop() {
	// Currently the client-level announcer will just see it was dropped when looking for work.
}
