package torrent

import (
	"io"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/time/rate"
)

func newRateLimitedReader(r io.Reader, l *rate.Limiter) io.Reader {
	if l == nil {
		// Avoids taking Limiter lock to check limit, and allows type assertions to bypass Read.
		return r
	}
	return rateLimitedReader{
		l: l,
		r: r,
	}
}

type rateLimitedReader struct {
	l *rate.Limiter
	r io.Reader
}

func (me rateLimitedReader) Read(b []byte) (n int, err error) {
	// Avoid truncating the read if everything is permitted anyway.
	if me.l.Limit() == rate.Inf {
		return me.r.Read(b)
	}
	// If the burst is zero, let the limiter method handle errors.
	if me.l.Burst() != 0 {
		b = b[:min(len(b), me.l.Burst())]
	}
	t := time.Now()
	n, err = me.r.Read(b)
	r := me.l.ReserveN(t, n)
	panicif.False(r.OK())
	time.Sleep(r.DelayFrom(t))
	return
}
