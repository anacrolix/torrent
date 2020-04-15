package torrent

import (
	"fmt"
	"net/url"

	"github.com/anacrolix/torrent/webtorrent"
)

type websocketTracker struct {
	url url.URL
	*webtorrent.TrackerClient
}

func (me websocketTracker) statusLine() string {
	return fmt.Sprintf("%q", me.url.String())
}

func (me websocketTracker) URL() url.URL {
	return me.url
}
