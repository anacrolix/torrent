package torrent

import (
	"golang.org/x/time/rate"
)

// 64 KiB used to be a rough default buffer for sockets on Windows. I'm sure it's bigger
// these days. What about the read buffer size mentioned elsewhere? Note this is also used for
// webseeding since that shares the download rate limiter by default.
const defaultDownloadRateLimiterBurst = 1 << 16

// Sets rate limiter burst if it's set to zero which is used to request the default by our API.
func setRateLimiterBurstIfZero(l *rate.Limiter, def int) {
	if l.Burst() == 0 && l.Limit() != rate.Inf {
		// What if the limit is greater than what can be represented by int?
		l.SetBurst(def)
	}
}
