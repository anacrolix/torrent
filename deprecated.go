package torrent

import (
	"github.com/anacrolix/torrent/webseed"
)

// Deprecated: The names doesn't reflect the actual behaviour. You can only add sources. Use
// AddSources instead.
func (t *Torrent) UseSources(sources []string) {
	t.AddSources(sources)
}

// Deprecated: Max requests are done Client-wide by host or cost-function applied to the webseed
// URL. Max concurrent requests to a WebSeed for a given torrent. This could easily be reimplemented
// by not assigning requests to a (Torrent, webseed URL) tuple keyed component in the webseed plan.
func WebSeedTorrentMaxRequests(maxRequests int) AddWebSeedsOpt {
	return func(c *webseed.Client) {
		c.MaxRequests = maxRequests
	}
}
