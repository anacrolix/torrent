package torrent

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/indexed"
	"github.com/anacrolix/torrent/tracker"
	trHttp "github.com/anacrolix/torrent/tracker/http"
)

// Designed in a way to allow switching to an event model if required. If multiple slots are allowed
// per tracker it would be handled here. Currently, handles only regular trackers but let's see if
// we can get websocket trackers to use this too.
type clientTrackerAnnouncer struct {
	trackerClient   tracker.Client
	torrentClient   *Client
	urlStr          trackerAnnouncerKey
	urlHost         string
	logger          *slog.Logger
	nextAnnounces   *indexed.Map[shortInfohash, announceValues]
	activeAnnounces int
	timer           *time.Timer
	timerWhen       time.Time
}

type nextAnnouncesRecord = indexed.Record[shortInfohash, announceValues]

func (me *clientTrackerAnnouncer) writeStatus(w io.Writer) {
	f := func(format string, args ...any) {
		fmt.Fprintf(w, format, args...)
	}
	f("Tracker: %q\n", me.urlStr)
	f("urlHost: %q, active announces %v, timer next: %v\n", me.urlHost, me.activeAnnounces, time.Until(me.timerWhen))
	f("Next announces:\n")
	sw := statusWriter{w: newIndentWriter(w, "  ")}
	tab := sw.tab()
	tab.cols("ShortInfohash", "Overdue", "Next", "Event", "NeedData", "WantPeers", "Webseeds", "Progress", "active")
	tab.row()
	for r := range me.nextAnnounces.Iter() {
		t := me.torrentClient.torrentsByShortHash[r.PrimaryKey]
		tab.cols(
			r.PrimaryKey,
			r.Values.overdue,
			time.Until(r.Values.When),
			r.Values.AnnounceEvent,
			r.Values.NeedData,
			r.Values.WantPeers,
			r.Values.HasActiveWebseedRequests,
		)
		tab.f("%d%%", int(100*t.progressUnitFloat()))
		tab.cols(r.Values.active)
		tab.row()
	}
	tab.end()
	sw.nl()
}

type nextAnnounceRecord = indexed.Record[shortInfohash, announceValues]

func (me *clientTrackerAnnouncer) init() {
	me.nextAnnounces = indexed.NewMap[shortInfohash, announceValues](
		compareNextAnnounce,
	)
	me.timer = time.AfterFunc(0, me.timerFunc)
	me.timerWhen = time.Now()
}

// This moves values that have When that have passed, so we compete on other parts of the priority
// if there is more than one pending. This can be done with another index, and have values move back
// the other way to simplify things.
func (me *clientTrackerAnnouncer) updateOverdue() {
	now := time.Now()
	it := me.nextAnnounces.Iterator()
	start := nextAnnounceRecord{}
	start.Values.overdue = false
	var overdue []shortInfohash
	// Avoid skipping lots of items with zero times because of trailing comparisons.
	it.SeekGE(start)
	//it.Next()
	for ; it.Valid(); it.Next() {
		vs := it.Cur().Values
		panicif.True(vs.overdue && !vs.active)
		if vs.When.After(now) {
			break
		}
		overdue = append(overdue, it.Cur().PrimaryKey)
	}
	for _, ih := range overdue {
		me.nextAnnounces.Update(ih, func(values *announceValues) {
			println("setting overdue:", ih.String(), values.When.String())
			values.overdue = true
		})
	}
}

func (me *clientTrackerAnnouncer) timerFunc() {
	me.timerWhen = time.Time{}
	me.torrentClient.lock()
	me.step()
	me.torrentClient.unlock()
}

func (me *clientTrackerAnnouncer) step() {
	me.dispatchAnnounces()
	// We *are* the Sen... Timer.
	panicif.True(me.resetTimer(me.nextTimerDelay()))
}

func (me *clientTrackerAnnouncer) resetTimer(d time.Duration) bool {
	me.timerWhen = time.Now().Add(d)
	return me.timer.Reset(d)
}

func nextAnnounceRecordToSorter(r nextAnnounceRecord) nextAnnounceSorter {
	return nextAnnounceSorter{
		ShortInfohash:  r.PrimaryKey,
		announceValues: r.Values,
	}
}

type torrentTrackerEvent struct {
	t      *Torrent
	urlStr string
}

func (me *clientTrackerAnnouncer) addedRegularTracker(event torrentTrackerEvent) {
	me.updateTorrentNextAnnounceValues(event.t)
}

const maxConcurrentAnnouncesPerTracker = 2

// Returns true if an announce was dispatched and should be tried again.
func (me *clientTrackerAnnouncer) dispatchAnnounces() {
	for {
		if me.activeAnnounces >= maxConcurrentAnnouncesPerTracker {
			return
		}
		nextOpt := me.getNextAnnounce()
		if !nextOpt.Ok {
			return
		}
		next := nextOpt.Value
		after := time.Until(next.When)
		me.logger.Debug("next announce", "after", after, "next", next)
		if after > 0 {
			return
		}
		me.startAnnounce(next.ShortInfohash)
	}
}

func (me *clientTrackerAnnouncer) startAnnounce(ih shortInfohash) {
	values := me.nextAnnounces.Get(ih).Unwrap()
	panicif.True(values.active)
	go me.singleAnnouncer(ih, values.AnnounceEvent)
	values.active = true
	me.activeAnnounces++
	me.nextAnnounces.Upsert(ih, values)
}

func (me *clientTrackerAnnouncer) finishedAnnounce(ih shortInfohash) {
	me.nextAnnounces.Update(ih, func(values *announceValues) {
		panicif.False(values.active)
		values.active = false
		values.overdue = false
		values.nextAnnounceValues = me.makeNextAnnounceValues(ih)
	})
	panicif.LessThanOrEqual(me.activeAnnounces, 0)
	me.activeAnnounces--
	me.updateTimer()
}

func (me *clientTrackerAnnouncer) updateTorrentNextAnnounceValues(t *Torrent) {
	for ih := range t.iterShortInfohashes() {
		values := me.makeNextAnnounceValues(ih)
		// Avoid clobbering active (non sorting state).
		me.nextAnnounces.CreateOrUpdate(ih, func(existed bool, av *announceValues) {
			av.nextAnnounceValues = values
		})
	}
	me.updateTimer()
}

func (me *clientTrackerAnnouncer) nextTimerDelay() time.Duration {
	next := me.getNextAnnounce()
	var d time.Duration = math.MaxInt64
	if next.Ok {
		d = time.Until(next.Value.When)
	}
	return d
}

func (me *clientTrackerAnnouncer) updateTimer() {
	if !me.timer.Stop() {
		return
	}
	// We should have been the one to stop it above, so we are responsible for starting it.
	panicif.True(me.resetTimer(me.nextTimerDelay()))
}

func (me *clientTrackerAnnouncer) singleAnnouncer(ih shortInfohash, event tracker.AnnounceEvent) {
	me.torrentClient.lock()
	defer me.torrentClient.unlock()
	defer me.finishedAnnounce(ih)
	t := me.torrentClient.torrentsByShortHash[ih]
	req := t.announceRequest(event, ih)
	ctx, cancel := context.WithTimeout(t.closedCtx, tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	// A logger that includes the nice torrent group so we know what the announce is for.
	logger := me.logger.With(
		t.slogGroup(),
		"short infohash", ih)
	me.torrentClient.unlock()
	logger.Debug("announcing", "req", req)
	resp, err := me.trackerClient.Announce(ctx, req, me.getAnnounceOpts())
	now := time.Now()
	logger.Debug("announced", "resp", resp, "err", err)
	me.torrentClient.lock()
	me.updateAnnounceState(ih, t, func(state *announceState) {
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

// Updates the announce state, shared by clientTrackerAnnouncer and Torrent, but it lives in Torrent
// for now.
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

func (me *clientTrackerAnnouncer) getNextAnnounce() (_ g.Option[nextAnnounceSorter]) {
	me.updateOverdue()
	for record := range me.nextAnnounces.Iter() {
		if record.Values.active {
			break
		}
		return g.Some(nextAnnounceSorter{
			ShortInfohash:  record.PrimaryKey,
			announceValues: record.Values,
		})
	}
	return
}

func (me *clientTrackerAnnouncer) makeNextAnnounceValues(ih [20]byte) nextAnnounceValues {
	panicif.Nil(me.torrentClient)
	t := me.torrentClient.torrentsByShortHash[ih]
	key := torrentTrackerAnnouncerKey{
		shortInfohash: ih,
		url:           me.urlStr,
	}
	state := t.regularTrackerAnnounceState[key]
	event, when := t.nextAnnounceEvent(key)
	return nextAnnounceValues{
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

func compareNextAnnounce(ar, br nextAnnounceRecord) int {
	// What about pushing back based on last announce failure? Some infohashes aren't liked by
	// trackers.
	return cmp.Or(
		compareBool(ar.Values.active, br.Values.active),
		-compareBool(ar.Values.overdue, br.Values.overdue),
		ar.Values.When.Compare(br.Values.When),
		-compareBool(ar.Values.WantPeers, br.Values.WantPeers),
		-compareBool(ar.Values.NeedData, br.Values.NeedData),
		-compareBool(ar.Values.HasActiveWebseedRequests, br.Values.HasActiveWebseedRequests),
		cmp.Compare(eventOrdering[ar.Values.AnnounceEvent], eventOrdering[br.Values.AnnounceEvent]),
	)
}

type nextAnnounceValues struct {
	When                     time.Time
	AnnounceEvent            tracker.AnnounceEvent
	NeedData                 bool
	WantPeers                bool
	HasActiveWebseedRequests bool
	LastAnnounceFailed       bool
}

type announceValues struct {
	active  bool
	overdue bool
	nextAnnounceValues
}

type nextAnnounceSorter struct {
	ShortInfohash shortInfohash
	announceValues
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
		return tracker.Started, time.Time{}
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

func (cl *Client) startTrackerAnnouncer(u *url.URL, urlStr trackerAnnouncerKey) {
	panicif.NotEq(u.String(), string(urlStr))
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
	tc, err := tracker.NewClient(string(urlStr), tracker.NewClientOpts{
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
	ta.init()
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
