package torrent

// Rate limiters per torrent, checked as well as the client's (ClientConfig.DownloadRateLimiter and
// UploadRateLimiter). The client's are shared by every connection, so one torrent with fast peers
// can take nearly all of a limit while another crawls. A torrent's own limiters let the caller give
// it a limit of its own, or share a global limit out between torrents.

import (
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// torrentRateLimiters are a torrent's own limiters. A nil one, or one
// with an infinite limit, limits nothing.
type torrentRateLimiters struct {
	down, up atomic.Pointer[rate.Limiter]
}

// SetRateLimiters gives the torrent download and upload limiters of its
// own; nil is none. Their limits may be changed while they're in use. As
// with the client's, the upload limiter's burst must hold a whole chunk
// (16 KiB) when its limit isn't infinite.
func (t *Torrent) SetRateLimiters(down, up *rate.Limiter) {
	t.rateLimiters.down.Store(down)
	t.rateLimiters.up.Store(up)
}

// limiting is l if it limits anything, else nil.
func limiting(l *rate.Limiter) *rate.Limiter {
	if l == nil || l.Limit() == rate.Inf {
		return nil
	}
	return l
}

// torrentLimitedReader limits a connection's reads by its torrent's
// download limiter, from when the connection knows its torrent (after the
// handshake). The client's limiter is applied by the reader it wraps.
//
// Unlike rateLimitedReader, the read is charged in pieces of at most the
// burst as it is when charging: the caller may shrink the burst while the
// read waits for data, and a reservation larger than the burst fails.
//
// The torrent's limiter isn't consulted before opening connections (the
// client's is): that is decided only when something
// happens (a peer is added, a connection closes) and doesn't ask again when
// the limiter refills, so a torrent in debt at that moment would stay
// without connections.
type torrentLimitedReader struct {
	c *PeerConn
	r io.Reader
}

func (me torrentLimitedReader) Read(b []byte) (int, error) {
	var l *rate.Limiter
	if t := me.c.limitedBy.Load(); t != nil {
		l = limiting(t.rateLimiters.down.Load())
	}
	if l == nil {
		return me.r.Read(b)
	}
	if burst := l.Burst(); burst > 0 {
		b = b[:min(len(b), burst)]
	}
	n, err := me.r.Read(b)
	var delay time.Duration
	now := time.Now()
	for left := n; left > 0; {
		step := min(left, max(l.Burst(), 1))
		r := l.ReserveN(now, step)
		if !r.OK() {
			break // the limit was taken away meanwhile
		}
		// Each reservation queues behind the one before, so the last
		// one's delay covers them all.
		delay = r.DelayFrom(now)
		left -= step
	}
	time.Sleep(delay)
	return n, err
}
