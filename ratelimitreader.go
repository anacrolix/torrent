package torrent

import (
	"io"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/time/rate"
)

type rateLimitedReader struct {
	l *rate.Limiter
	r io.Reader
}

func (me *rateLimitedReader) Read(b []byte) (n int, err error) {
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
