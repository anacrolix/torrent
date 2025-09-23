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

type trackerAnnouncer struct {
	trackerClient tracker.Client
	torrentClient *Client
	u             url.URL
	logger        *slog.Logger
	cond          chansync.BroadcastCond
}

func (me *trackerAnnouncer) Run() {
	me.torrentClient.lock()
	for {
		next := me.getNextAnnounce()
		after := time.Until(next.Value.When)
		me.logger.Debug("next announce", "after", after, "when", next.Value.When)
		if next.Ok && after <= 0 {
			me.announce(next.Value)
			continue
		}
		cond := me.cond.Signaled()
		me.torrentClient.unlock()
		var afterChan <-chan time.Time
		if next.Ok {
			afterChan = time.After(after)
		}
		select {
		case <-me.torrentClient.closed.Done():
			return
		case <-afterChan:
		case <-cond:
		}
		me.torrentClient.lock()
	}
}

func (me *trackerAnnouncer) announce(next nextAnnounce) {
	t := me.torrentClient.torrentsByShortHash[next.ShortInfohash]
	req := t.announceRequest(next.AnnounceEvent, next.ShortInfohash)
	ctx, cancel := context.WithTimeout(t.closedCtx, tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	me.torrentClient.unlock()
	me.logger.Debug("announcing", "short infohash", next.ShortInfohash)
	resp, err := me.trackerClient.Announce(ctx, req, me.getAnnounceOpts())
	now := time.Now()
	me.logger.Debug("announced", "resp", resp, "err", err)
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
		me.logger.Log(ctx, level, "announce failed", "err", err)
		return
	} else {
		me.logger.Debug("announce returned", "numPeers", len(resp.Peers))
	}
	t.addPeers(peerInfos(nil).AppendFromTracker(resp.Peers))
}

func (me *trackerAnnouncer) updateAnnounceState(ih shortInfohash, t *Torrent, update func(state *announceState)) {
	key := torrentTrackerAnnouncerKey{
		shortInfohash: ih,
		url:           me.u.String(),
	}
	as := t.regularTrackerAnnounceState[key]
	update(&as)
	g.MakeMapIfNil(&t.regularTrackerAnnounceState)
	t.regularTrackerAnnounceState[key] = as
}

func (me *trackerAnnouncer) getAnnounceOpts() trHttp.AnnounceOpt {
	cfg := me.torrentClient.config
	return trHttp.AnnounceOpt{
		UserAgent:           cfg.HTTPUserAgent,
		HostHeader:          me.u.Host,
		ClientIp4:           cfg.PublicIp4,
		ClientIp6:           cfg.PublicIp6,
		HttpRequestDirector: cfg.HttpRequestDirector,
	}
}

func (me *trackerAnnouncer) getNextAnnounce() (best g.Option[nextAnnounce]) {
	for ih, t := range me.torrentClient.torrentsByShortHash {
		key := torrentTrackerAnnouncerKey{
			shortInfohash: ih,
			url:           me.u.String(),
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

func (me *trackerAnnouncer) torrentNextAnnounce(ih [20]byte) nextAnnounce {
	t := me.torrentClient.torrentsByShortHash[ih]
	key := torrentTrackerAnnouncerKey{
		shortInfohash: ih,
		url:           me.u.String(),
	}
	event, when := t.nextAnnounceEvent(key)
	return nextAnnounce{
		ShortInfohash: ih,
		AnnounceEvent: event,
		When:          when,
		NeedPeers:     false, // TODO
	}
}

// Make zero/default unhandled AnnounceEvent sort last.
var eventOrdering = map[tracker.AnnounceEvent]int{
	tracker.Started:   -4, // Get peers ASAP
	tracker.Stopped:   -3, // Stop unwanted peers ASAP
	tracker.Completed: -2, // Maybe prevent seeders from connecting to us
	tracker.None:      -1, // Regular maintenance
}

func (me *trackerAnnouncer) compareNextAnnounce(a, b nextAnnounce) int {
	return cmp.Or(
		a.When.Compare(b.When),
		-compareBool(a.NeedPeers, b.NeedPeers),
		cmp.Compare(eventOrdering[a.AnnounceEvent], eventOrdering[b.AnnounceEvent]),
	)
}

type nextAnnounce struct {
	ShortInfohash [20]byte
	AnnounceEvent tracker.AnnounceEvent
	When          time.Time
	NeedPeers     bool
}

func (me *Torrent) nextAnnounceEvent(key torrentTrackerAnnouncerKey) (event tracker.AnnounceEvent, when time.Time) {
	state := me.regularTrackerAnnounceState[key]
	// Extend when if there was an error on the last attempt.
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
	ta := &trackerAnnouncer{
		trackerClient: tc,
		torrentClient: cl,
		u:             *u, // Paranoid, I don't trust Go mutability
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
