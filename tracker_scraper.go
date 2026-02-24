package torrent

import (
	"bytes"
	"fmt"
	"net/url"
	"time"
)

type torrentTrackerAnnouncer interface {
	statusLine() string
	URL() *url.URL

	Stop()
}

func regularTrackerScraperStatusLine(state announceState) string {
	lastAnnounce := state.lastOk
	var w bytes.Buffer
	fmt.Fprintf(&w, "next ann: %v, last ann: %v",
		func() string {
			na := time.Until(lastAnnounce.Completed.Add(lastAnnounce.Interval))
			if na > 0 {
				na /= time.Second
				na *= time.Second
				return na.String()
			} else {
				return "anytime"
			}
		}(),
		func() string {
			if state.Err != nil {
				return state.Err.Error()
			}
			if lastAnnounce.Completed.IsZero() {
				return "never"
			}
			return fmt.Sprintf("%d peers", lastAnnounce.NumPeers)
		}(),
	)
	return w.String()
}
