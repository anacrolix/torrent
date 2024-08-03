package torrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
)

// Announces a torrent to a tracker at regular intervals, when peers are
// required.
type trackerScraper struct {
	u               url.URL
	t               *Torrent
	mu              sync.RWMutex
	lastAnnounce    trackerAnnounceResult
	lookupTrackerIp func(*url.URL) ([]net.IP, error)
	ips             []net.IP
	lastIpLookup    time.Time
}

type torrentTrackerAnnouncer interface {
	statusLine() string
	URL() *url.URL
}

func (me *trackerScraper) URL() *url.URL {
	return &me.u
}

func (ts *trackerScraper) statusLine() string {
	var w bytes.Buffer
	fmt.Fprintf(&w, "next ann: %v, last ann: %v",
		func() string {
			ts.mu.RLock()
			defer ts.mu.RUnlock()
			na := time.Until(ts.lastAnnounce.Completed.Add(ts.lastAnnounce.Interval))
			if na > 0 {
				na /= time.Second
				na *= time.Second
				return na.String()
			} else {
				return "anytime"
			}
		}(),
		func() string {
			ts.mu.RLock()
			defer ts.mu.RUnlock()
			if ts.lastAnnounce.Err != nil {
				return ts.lastAnnounce.Err.Error()
			}
			if ts.lastAnnounce.Completed.IsZero() {
				return "never"
			}
			return fmt.Sprintf("%d peers", ts.lastAnnounce.NumPeers)
		}(),
	)
	return w.String()
}

type trackerAnnounceResult struct {
	Err       error
	NumPeers  int
	Interval  time.Duration
	Completed time.Time
}

func (me *trackerScraper) getIp() (ip net.IP, err error) {
	// cache the ip lookup for 15 mins, this avoids
	// spamming DNS on os's that don't cache DNS lookups
	//  Cache TTL between 1 and 6 hours

	if len(me.ips) == 0 || time.Since(me.lastIpLookup) > time.Hour+time.Duration(rand.Int63n(int64(5*time.Hour))) {
		me.lastIpLookup = time.Now()
		if me.lookupTrackerIp != nil {
			me.ips, err = me.lookupTrackerIp(&me.u)
		} else {
			// Do a regular dns lookup
			me.ips, err = net.LookupIP(me.u.Hostname())
		}
		if err != nil {
			return
		}
	}

	if len(me.ips) == 0 {
		err = errors.New("no ips")
		return
	}
	me.t.cl.rLock()
	defer me.t.cl.rUnlock()
	if me.t.cl.closed.IsSet() {
		err = errors.New("client is closed")
		return
	}
	for _, ip = range me.ips {
		if me.t.cl.ipIsBlocked(ip) {
			continue
		}
		switch me.u.Scheme {
		case "udp4":
			if ip.To4() == nil {
				continue
			}
		case "udp6":
			if ip.To4() != nil {
				continue
			}
		}
		return
	}

	me.ips = nil
	err = errors.New("no acceptable ips")
	return
}

func (me *trackerScraper) trackerUrl(ip net.IP) string {
	u := me.u
	if u.Port() != "" {
		u.Host = net.JoinHostPort(ip.String(), u.Port())
	}
	return u.String()
}

// Return how long to wait before trying again. For most errors, we return 5
// minutes, a relatively quick turn around for DNS changes.
func (me *trackerScraper) announce(ctx context.Context, event tracker.AnnounceEvent) (ret trackerAnnounceResult) {
	defer func() {
		ret.Completed = time.Now()
	}()
	ret.Interval = time.Minute

	// Limit concurrent use of the same tracker URL by the Client.
	ref := me.t.cl.activeAnnounceLimiter.GetRef(me.u.String())
	defer ref.Drop()
	select {
	case <-ctx.Done():
		ret.Err = ctx.Err()
		return
	case ref.C() <- struct{}{}:
	}
	defer func() {
		select {
		case <-ref.C():
		default:
			panic("should return immediately")
		}
	}()

	ip, err := me.getIp()
	if err != nil {
		ret.Err = fmt.Errorf("error getting ip: %s", err)
		return
	}
	req := me.t.announceRequest(event, true, true)
	// The default timeout works well as backpressure on concurrent access to the tracker. Since
	// we're passing our own Context now, we will include that timeout ourselves to maintain similar
	// behavior to previously, albeit with this context now being cancelled when the Torrent is
	// closed.
	ctx, cancel := context.WithTimeout(ctx, tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	me.t.logger.WithDefaultLevel(log.Debug).Printf("announcing to %q: %#v", me.u.String(), req)
	res, err := tracker.Announce{
		Context:             ctx,
		HttpProxy:           me.t.cl.config.HTTPProxy,
		HttpRequestDirector: me.t.cl.config.HttpRequestDirector,
		DialContext:         me.t.cl.config.TrackerDialContext,
		ListenPacket:        me.t.cl.config.TrackerListenPacket,
		UserAgent:           me.t.cl.config.HTTPUserAgent,
		TrackerUrl:          me.trackerUrl(ip),
		Request:             req,
		HostHeader:          me.u.Host,
		ServerName:          me.u.Hostname(),
		UdpNetwork:          me.u.Scheme,
		ClientIp4:           krpc.NodeAddr{IP: me.t.cl.config.PublicIp4},
		ClientIp6:           krpc.NodeAddr{IP: me.t.cl.config.PublicIp6},
		Logger:              me.t.logger,
	}.Do()
	me.t.logger.WithDefaultLevel(log.Debug).Printf("announce to %q returned %#v: %v", me.u.String(), res, err)
	if err != nil {
		ret.Err = fmt.Errorf("announcing: %w", err)
		return
	}
	me.t.AddPeers(peerInfos(nil).AppendFromTracker(res.Peers))
	ret.NumPeers = len(res.Peers)
	ret.Interval = time.Duration(res.Interval) * time.Second
	return
}

// Returns whether we can shorten the interval, and sets notify to a channel that receives when we
// might change our mind, or leaves it if we won't.
func (me *trackerScraper) canIgnoreInterval(notify *<-chan struct{}) bool {
	gotInfo := me.t.GotInfo()
	select {
	case <-gotInfo:
		// Private trackers really don't like us announcing more than they specify. They're also
		// tracking us very carefully, so it's best to comply.
		private := me.t.info.Private
		return private == nil || !*private
	default:
		*notify = gotInfo
		return false
	}
}

func (me *trackerScraper) Run() {
	defer me.announceStopped()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer cancel()
		select {
		case <-ctx.Done():
		case <-me.t.Closed():
		}
	}()

	// make sure first announce is a "started"
	e := tracker.Started

	for {
		ar := me.announce(ctx, e)
		// after first announce, get back to regular "none"
		e = tracker.None
		me.mu.Lock()
		me.lastAnnounce = ar
		me.mu.Unlock()

	recalculate:
		// Make sure we don't announce for at least a minute since the last one.
		interval := ar.Interval
		if interval < time.Minute {
			interval = time.Minute
		}

		me.t.mu.Lock()
		wantPeers := me.t.wantPeersEvent.C()
		me.t.mu.Unlock()

		// If we want peers, reduce the interval to the minimum if it's appropriate.

		// A channel that receives when we should reconsider our interval. Starts as nil since that
		// never receives.
		var reconsider <-chan struct{}
		select {
		case <-wantPeers:
			if interval > time.Minute && me.canIgnoreInterval(&reconsider) {
				interval = time.Minute
			}
		default:
			reconsider = wantPeers
		}

		select {
		case <-me.t.closed.Done():
			return
		case <-reconsider:
			// Recalculate the interval.
			goto recalculate
		case <-time.After(time.Until(ar.Completed.Add(interval))):
		}
	}
}

func (me *trackerScraper) announceStopped() {
	ctx, cancel := context.WithTimeout(context.Background(), tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	me.announce(ctx, tracker.Stopped)
}
