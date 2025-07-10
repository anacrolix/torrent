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

	// This is the time of the last Read's reservation.
	lastRead time.Time
}

func (me *rateLimitedReader) justRead(b []byte) (n int, err error) {
	n, err = me.r.Read(b)
	me.lastRead = time.Now()
	return
}

func (me *rateLimitedReader) Read(b []byte) (n int, err error) {
	if me.l.Burst() != 0 {
		b = b[:min(len(b), me.l.Burst())]
	}
	t := time.Now()
	n, err = me.justRead(b)
	r := me.l.ReserveN(t, n)
	panicif.False(r.OK())
	time.Sleep(r.DelayFrom(t))
	return
}
