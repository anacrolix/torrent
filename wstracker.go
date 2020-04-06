package torrent

import (
	"fmt"
	"net/url"
)

type websocketTracker struct {
	url url.URL
}

func (me websocketTracker) statusLine() string {
	return fmt.Sprintf("%q", me.url.String())
}

func (me websocketTracker) URL() url.URL {
	return me.url
}
