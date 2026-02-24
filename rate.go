package torrent

import (
	"math"

	"golang.org/x/time/rate"
)

// 64 KiB used to be a rough default buffer for sockets on Windows. I'm sure it's bigger these days.
// What about the read buffer size mentioned elsewhere? Note this is also used for webseeding since
// that shares the download rate limiter by default. 1 MiB is the default max read frame size for
// HTTP/2,
const defaultMinDownloadRateLimiterBurst = 1 << 20

// Sets rate limiter burst if it's set to zero which is used to request the default by our API.
func setRateLimiterBurstIfZero(l *rate.Limiter, def int) {
	// Set it to something reasonable if the limit is Inf, in case the limit is dynamically adjusted
	// and the user doesn't know what value to use. Assume the original limit is in a reasonable
	// ballpark.
	if l != nil && l.Burst() == 0 {
		// What if the limit is greater than what can be represented by int?
		l.SetBurst(def)
	}
}

// Sets rate limiter burst if it's set to zero which is used to request the default by our API.
func setDefaultDownloadRateLimiterBurstIfZero(l *rate.Limiter) {
	setRateLimiterBurstIfZero(l, int(
		max(
			min(EffectiveDownloadRateLimit(l), math.MaxInt),
			defaultMinDownloadRateLimiterBurst)))
}
