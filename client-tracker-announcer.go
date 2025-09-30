package torrent

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/tracker"
	trHttp "github.com/anacrolix/torrent/tracker/http"
)

// Designed in a way to allow switching to an event model if required. If multiple slots are allowed
// per tracker it would be handled here. Currently handles only regular trackers but lets see if we
// can get websocket trackers to use this too.
type clientTrackerAnnouncer struct {
	trackerClient tracker.Client
	torrentClient *Client
	urlStr        string
	urlHost       string
	logger        *slog.Logger
	cond          chansync.BroadcastCond
}

func (me *clientTrackerAnnouncer) Run() {
	me.torrentClient.lock()
	for {
		next := me.getNextAnnounce()
		var afterChan <-chan time.Time
		if next.Ok {
			after := time.Until(next.Value.When)
			me.logger.Debug("next announce", "after", after, "next", next.Value)
			if next.Ok && after <= 0 {
				me.announce(next.Value)
				continue
			}
			afterChan = time.After(after)
		}
		cond := me.cond.Signaled()
		me.torrentClient.unlock()
		select {
		case <-me.torrentClient.closed.Done():
			return
		case <-afterChan:
		case <-cond:
		}
		me.torrentClient.lock()
	}
}

func (me *clientTrackerAnnouncer) announce(next nextAnnounceSorter) {
	t := me.torrentClient.torrentsByShortHash[next.ShortInfohash]
	req := t.announceRequest(next.AnnounceEvent, next.ShortInfohash)
	ctx, cancel := context.WithTimeout(t.closedCtx, tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	// A logger that includes the nice torrent group so we know what the announce is for.
	logger := me.logger.With(
		t.slogGroup(),
		"short infohash", next.ShortInfohash)
	me.torrentClient.unlock()
	logger.Debug("announcing", "req", req)
	resp, err := me.trackerClient.Announce(ctx, req, me.getAnnounceOpts())
	now := time.Now()
	logger.Debug("announced", "resp", resp, "err", err)
	me.torrentClient.lock()
	me.updateAnnounceState(next.ShortInfohash, t, func(state *announceState) {
		state.Err = err
		state.lastAttemptCompleted = now
		if err == nil {
			state.lastOk = lastAnnounceOk{
				AnnouncedEvent: req.Event,
				Interval:       time.Duration(resp.Interval) * time.Second,
				NumPeers:       len(resp.Peers),
				Completed:      now,
			}
		}
	})
	if err != nil {
		level := slog.LevelWarn
		if ctx.Err() != nil {
			level = slog.LevelDebug
		}
		logger.Log(ctx, level, "announce failed", "err", err)
		return
	} else {
		logger.Debug("announce returned", "numPeers", len(resp.Peers))
	}
	t.addPeers(peerInfos(nil).AppendFromTracker(resp.Peers))
}

func (me *clientTrackerAnnouncer) updateAnnounceState(ih shortInfohash, t *Torrent, update func(state *announceState)) {
	key := torrentTrackerAnnouncerKey{
		shortInfohash: ih,
		url:           me.urlStr,
	}
	as := t.regularTrackerAnnounceState[key]
	update(&as)
	g.MakeMapIfNil(&t.regularTrackerAnnounceState)
	t.regularTrackerAnnounceState[key] = as
}

func (me *clientTrackerAnnouncer) getAnnounceOpts() trHttp.AnnounceOpt {
	cfg := me.torrentClient.config
	return trHttp.AnnounceOpt{
		UserAgent:           cfg.HTTPUserAgent,
		HostHeader:          me.urlHost,
		ClientIp4:           cfg.PublicIp4,
		ClientIp6:           cfg.PublicIp6,
		HttpRequestDirector: cfg.HttpRequestDirector,
	}
}

func (me *clientTrackerAnnouncer) getNextAnnounce() (best g.Option[nextAnnounceSorter]) {
	for ih, t := range me.torrentClient.torrentsByShortHash {
		key := torrentTrackerAnnouncerKey{
			shortInfohash: ih,
			url:           me.urlStr,
		}
		if !g.MapContains(t.trackerAnnouncers, key) {
			continue
		}
		cur := me.torrentNextAnnounce(ih)
		if !best.Ok || me.compareNextAnnounce(cur, best.Value) < 0 {
			best.Set(cur)
		}
	}
	return
}

func (me *clientTrackerAnnouncer) torrentNextAnnounce(ih [20]byte) nextAnnounceSorter {
	t := me.torrentClient.torrentsByShortHash[ih]
	key := torrentTrackerAnnouncerKey{
		shortInfohash: ih,
		url:           me.urlStr,
	}
	state := t.regularTrackerAnnounceState[key]
	event, when := t.nextAnnounceEvent(key)
	return nextAnnounceSorter{
		ShortInfohash:            ih,
		t:                        t,
		AnnounceEvent:            event,
		When:                     when,
		LastAnnounceFailed:       state.Err != nil,
		NeedData:                 t.needData(),
		WantPeers:                t.wantPeers(),
		HasActiveWebseedRequests: t.hasActiveWebseedRequests(),
	}
}

// Make zero/default unhandled AnnounceEvent sort last.
var eventOrdering = map[tracker.AnnounceEvent]int{
	tracker.Started: -4, // Get peers ASAP
	tracker.Stopped: -3, // Stop unwanted peers ASAP
	// Maybe prevent seeders from connecting to us. We want to send this before Stopped, but also we
	// don't want people connecting to us if we are stopped and can only get out a single message.
	// Really we should send this before Stopped...
	tracker.Completed: -2,
	tracker.None:      -1, // Regular maintenance
}

func (me *clientTrackerAnnouncer) compareNextAnnounce(a, b nextAnnounceSorter) int {
	// What about pushing back based on last announce failure? Some infohashes aren't liked by
	// trackers.
	return cmp.Or(
		a.When.Compare(b.When),
		-compareBool(a.WantPeers, b.WantPeers),
		-compareBool(a.NeedData, b.NeedData),
		-compareBool(a.HasActiveWebseedRequests, b.HasActiveWebseedRequests),
		cmp.Compare(eventOrdering[a.AnnounceEvent], eventOrdering[b.AnnounceEvent]),
	)
}

type nextAnnounceSorter struct {
	ShortInfohash shortInfohash
	t             *Torrent

	When                     time.Time
	AnnounceEvent            tracker.AnnounceEvent
	NeedData                 bool
	WantPeers                bool
	HasActiveWebseedRequests bool
	LastAnnounceFailed       bool
}

func (me *Torrent) nextAnnounceEvent(key torrentTrackerAnnouncerKey) (event tracker.AnnounceEvent, when time.Time) {
	state := me.regularTrackerAnnounceState[key]
	// Extend `when` if there was an error on the last attempt.
	defer func() {
		if state.Err != nil {
			minWhen := time.Now().Add(time.Minute)
			if when.Before(minWhen) {
				when = minWhen
			}
		}
	}()
	last := state.lastOk
	if last.Completed.IsZero() {
		return tracker.Started, time.Now()
	}
	// TODO: Shorten and modify intervals here. Check for completion/stopped etc.
	return tracker.None, last.Completed.Add(last.Interval)
}

type lastAnnounceOk struct {
	AnnouncedEvent tracker.AnnounceEvent
	Interval       time.Duration
	Completed      time.Time
	NumPeers       int
}

type announceState struct {
	lastOk               lastAnnounceOk
	Err                  error
	lastAttemptCompleted time.Time
}

func (cl *Client) startTrackerAnnouncer(u *url.URL, urlStr string) {
	panicif.NotEq(u.String(), urlStr)
	if g.MapContains(cl.regularTrackerAnnouncers, urlStr) {
		return
	}
	// Parts of the old Announce code, here for reference, to help with mapping configuration to the
	// new global client tracker implementation.
	/*
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

		cl, err := NewClient(me.TrackerUrl, NewClientOpts{
			Http: trHttp.NewClientOpts{
				Proxy:       me.HttpProxy,
				DialContext: me.DialContext,
				ServerName:  me.ServerName,
			},
			UdpNetwork:   me.UdpNetwork,
			Logger:       me.Logger.WithContextValue(fmt.Sprintf("tracker client for %q", me.TrackerUrl)),
			ListenPacket: me.ListenPacket,
		})
		if err != nil {
			return
		}
		defer cl.Close()
		if me.Context == nil {
			// This is just to maintain the old behaviour that should be a timeout of 15s. Users can
			// override it by providing their own Context. See comments elsewhere about longer timeouts
			// acting as rate limiting overloaded trackers.
			ctx, cancel := context.WithTimeout(context.Background(), DefaultTrackerAnnounceTimeout)
			defer cancel()
			me.Context = ctx
		}
		return cl.Announce(me.Context, me.Request, trHttp.AnnounceOpt{
			UserAgent:           me.UserAgent,
			HostHeader:          me.HostHeader,
			ClientIp4:           me.ClientIp4.IP,
			ClientIp6:           me.ClientIp6.IP,
			HttpRequestDirector: me.HttpRequestDirector,
		})
	*/
	tc, err := tracker.NewClient(urlStr, tracker.NewClientOpts{
		Http: trHttp.NewClientOpts{
			Proxy:       cl.config.HTTPProxy,
			DialContext: cl.config.TrackerDialContext,
			ServerName:  u.Hostname(),
		},
		UdpNetwork:   u.Scheme,
		Logger:       cl.logger.WithContextValue(fmt.Sprintf("tracker client for %q", urlStr)),
		ListenPacket: cl.config.TrackerListenPacket,
	})
	panicif.Err(err)
	// Need deep copy
	panicif.NotNil(u.User)
	ta := &clientTrackerAnnouncer{
		trackerClient: tc,
		torrentClient: cl,
		urlStr:        urlStr,
		urlHost:       u.Host,
		logger:        cl.slogger.With("tracker", u.String()),
	}
	g.MakeMapIfNil(&cl.regularTrackerAnnouncers)
	g.MapMustAssignNew(cl.regularTrackerAnnouncers, urlStr, ta)
	go ta.Run()
}

type regularTrackerAnnouncer struct {
	u                *url.URL
	getAnnounceState func() announceState
}

func (r regularTrackerAnnouncer) statusLine() string {
	return regularTrackerScraperStatusLine(r.getAnnounceState())
}

func (r regularTrackerAnnouncer) URL() *url.URL {
	return r.u
}

func (r regularTrackerAnnouncer) Stop() {
	// Currently the client-level announcer will just see it was dropped when looking for work.
}

var _ torrentTrackerAnnouncer = regularTrackerAnnouncer{}
