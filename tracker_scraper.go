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
	if err != nil {
		ret.Err = fmt.Errorf("announcing: %w", err)
		return
	}
	me.t.AddPeers(peerInfos(nil).AppendFromTracker(res.Peers))
	ret.NumPeers = len(res.Peers)
	ret.Interval = time.Duration(res.Interval) * time.Second
	return
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

	wait:
		// Make sure we don't announce for at least a minute since the last one.
		interval := ar.Interval
		if interval < time.Minute {
			interval = time.Minute
		}

		me.t.cl.lock()
		wantPeers := me.t.wantPeersEvent.C()
		closed := me.t.closed.C()
		me.t.cl.unlock()

		// If we want peers, reduce the interval to the minimum.
		select {
		case <-wantPeers:
			if interval > time.Minute {
				interval = time.Minute
			}
			// Now we're at the minimum, don't trigger on it anymore.
			wantPeers = nil
		default:
		}

		select {
		case <-closed:
			return
		case <-wantPeers:
			// Recalculate the interval.
			goto wait
		case <-time.After(time.Until(ar.Completed.Add(interval))):
		}
	}
}

func (me *trackerScraper) announceStopped() {
	me.announce(tracker.Stopped)
}
