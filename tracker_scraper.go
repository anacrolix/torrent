package torrent

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
)

// Announces a torrent to a tracker at regular intervals, when peers are
// required.
type trackerScraper struct {
	u            url.URL
	t            *Torrent
	lastAnnounce trackerAnnounceResult
	allow        func()
	// The slowdown argument lets us indicate if we think there should be some backpressure on
	// access to the tracker. It doesn't necessarily have to be used.
	done func(slowdown bool)
}

type torrentTrackerAnnouncer interface {
	statusLine() string
	URL() *url.URL
}

func (me trackerScraper) URL() *url.URL {
	return &me.u
}

func (ts *trackerScraper) statusLine() string {
	var w bytes.Buffer
	fmt.Fprintf(&w, "next ann: %v, last ann: %v",
		func() string {
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
	ips, err := net.LookupIP(me.u.Hostname())
	if err != nil {
		return
	}
	if len(ips) == 0 {
		err = errors.New("no ips")
		return
	}
	for _, ip = range ips {
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
func (me *trackerScraper) announce(event tracker.AnnounceEvent) (ret trackerAnnounceResult) {
	defer func() {
		ret.Completed = time.Now()
	}()
	ret.Interval = time.Minute
	me.allow()
	// We might pass true if we got an error. Currently we don't because timing out with a
	// reasonably long timeout is its own form of backpressure (it remains to be seen if it's
	// enough).
	defer me.done(false)
	ip, err := me.getIp()
	if err != nil {
		ret.Err = fmt.Errorf("error getting ip: %s", err)
		return
	}
	me.t.cl.rLock()
	req := me.t.announceRequest(event)
	me.t.cl.rUnlock()
	// The default timeout is currently 15s, and that works well as backpressure on concurrent
	// access to the tracker.
	//ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	//defer cancel()
	me.t.logger.WithDefaultLevel(log.Debug).Printf("announcing to %q: %#v", me.u.String(), req)
	res, err := tracker.Announce{
		//Context:    ctx,
		HTTPProxy:  me.t.cl.config.HTTPProxy,
		UserAgent:  me.t.cl.config.HTTPUserAgent,
		TrackerUrl: me.trackerUrl(ip),
		Request:    req,
		HostHeader: me.u.Host,
		ServerName: me.u.Hostname(),
		UdpNetwork: me.u.Scheme,
		ClientIp4:  krpc.NodeAddr{IP: me.t.cl.config.PublicIp4},
		ClientIp6:  krpc.NodeAddr{IP: me.t.cl.config.PublicIp6},
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
	// make sure first announce is a "started"
	e := tracker.Started
	for {
		ar := me.announce(e)
		// after first announce, get back to regular "none"
		e = tracker.None
		me.t.cl.lock()
		me.lastAnnounce = ar
		me.t.cl.unlock()

	recalculate:
		// Make sure we don't announce for at least a minute since the last one.
		interval := ar.Interval
		if interval < time.Minute {
			interval = time.Minute
		}

		me.t.cl.lock()
		wantPeers := me.t.wantPeersEvent.C()
		closed := me.t.closed.C()
		me.t.cl.unlock()

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
		case <-closed:
			return
		case <-reconsider:
			// Recalculate the interval.
			goto recalculate
		case <-time.After(time.Until(ar.Completed.Add(interval))):
		}
	}
}

func (me *trackerScraper) announceStopped() {
	me.announce(tracker.Stopped)
}
