package torrent

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	g "github.com/anacrolix/generics"
	analog "github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/extracmp"
	"github.com/anacrolix/torrent/internal/indexed"
	"github.com/anacrolix/torrent/internal/mytimer"
	"github.com/anacrolix/torrent/tracker"
	trHttp "github.com/anacrolix/torrent/tracker/http"
)

// Designed in a way to allow switching to an event model if required. If multiple slots are allowed
// per tracker it would be handled here. Currently, handles only regular trackers but let's see if
// we can get websocket trackers to use this too.
type regularTrackerAnnounceDispatcher struct {
	trackerClients map[trackerAnnouncerKey]*trackerClientsValue
	torrentClient  *Client
	logger         *slog.Logger

	// Raw announce data keyed by announcer and short infohash.
	announceData indexed.Map[torrentTrackerAnnouncerKey, nextAnnounceInput]
	// Announcing sorted by url then priority.
	announceIndex indexed.Index[nextAnnounceRecord]
	overdueIndex  indexed.Index[nextAnnounceRecord]

	trackerAnnounceHead indexed.Table[trackerAnnounceHeadRecord]
	nextAnnounce        indexed.Index[trackerAnnounceHeadRecord]

	infohashAnnouncing indexed.Map[shortInfohash, infohashConcurrency]
	trackerAnnouncing  indexed.Map[trackerAnnouncerKey, int]
	timer              mytimer.Timer
}

type trackerAnnounceHeadRecord struct {
	trackerRequests int
	nextAnnounceRecord
}

type trackerClientsValue struct {
	client tracker.Client
	//active int
}

// According to compareNextAnnounce, which is universal, and we only need to handle the non-zero
// value fields.
func nextAnnounceMinRecord() (gte nextAnnounceRecord) {
	gte.overdue = true
	gte.torrent.Ok = true
	gte.torrent.Value.NeedData = true
	gte.torrent.Value.WantPeers = true
	gte.AnnounceEvent = tracker.Started
	return
}

func (me *regularTrackerAnnounceDispatcher) init(client *Client) {
	me.torrentClient = client
	me.logger = client.slogger
	me.announceData.Init(torrentTrackerAnnouncerKey.Compare)
	me.announceData.OnChange(func(old, new g.Option[indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]]) {
		if !new.Ok {
			return
		}
		panicif.True(new.Value.Right.When.IsZero())
		panicif.NotEq(
			new.Value.Right.infohashActive,
			g.OptionFromTuple(me.infohashAnnouncing.Get(new.Value.Left.ShortInfohash)).Value.count)
	})
	me.announceIndex = indexed.NewFullMappedIndex(&me.announceData, announceIndexCompare, nextAnnounceRecordFromPair)
	me.announceIndex.SetMinRecord(nextAnnounceMinRecord())
	me.overdueIndex = indexed.NewFullMappedIndex(&me.announceData, overdueIndexCompare, nextAnnounceRecordFromPair)
	me.overdueIndex.SetMinRecord(func() (ret nextAnnounceRecord) {
		ret.overdue = true
		return
	}())
	me.trackerAnnounceHead.Init(func(a, b trackerAnnounceHeadRecord) int {
		return cmp.Compare(a.url, b.url)
	})
	me.nextAnnounce = indexed.NewFullIndex(
		&me.trackerAnnounceHead,
		func(a, b trackerAnnounceHeadRecord) int {
			return cmp.Or(
				cmp.Compare(a.trackerRequests, b.trackerRequests),
				compareNextAnnounce(a.nextAnnounceInput, b.nextAnnounceInput),
				a.torrentTrackerAnnouncerKey.Compare(b.torrentTrackerAnnouncerKey),
			)
		},
	)
	// After announce index changes (we need the ordering), update the next announce for each
	// tracker url.
	me.announceData.OnChange(func(old, new g.Option[indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]]) {
		if old.Ok {
			me.updateTrackerAnnounceHead(old.Value.Left.url)
		}
		if new.Ok {
			me.updateTrackerAnnounceHead(new.Value.Left.url)
		}
	})
	me.infohashAnnouncing.Init(shortInfohash.Compare)
	me.infohashAnnouncing.OnValueChange(func(shortIh shortInfohash, old, new g.Option[infohashConcurrency]) {
		start := me.announceData.MinRecord()
		start.Left.ShortInfohash = shortIh
		keys := make([]torrentTrackerAnnouncerKey, 0, len(me.trackerClients))
		var expectedCount g.Option[int]
		for r := range indexed.IterClusteredWhere(
			me.announceData,
			start,
			func(p indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]) bool {
				return p.Left.ShortInfohash == shortIh
			},
		) {
			if expectedCount.Ok {
				panicif.NotEq(r.Right.infohashActive, expectedCount.Value)
			} else {
				expectedCount.Set(r.Right.infohashActive)
			}
			if r.Right.infohashActive != new.Value.count {
				keys = append(keys, r.Left)
			}
		}
		for _, key := range keys {
			panicif.False(me.announceData.Update(
				key,
				func(input nextAnnounceInput) nextAnnounceInput {
					input.infohashActive = new.Value.count
					return input
				},
			))
		}
	})
	me.trackerAnnouncing.Init(cmp.Compare)
	me.trackerAnnouncing.OnValueChange(func(key trackerAnnouncerKey, old, new g.Option[int]) {
		panicif.GreaterThan(new.Value, maxConcurrentAnnouncesPerTracker)
		me.updateTrackerAnnounceHead(key)
		// This could be modified to use "instead of" triggers, or alter the new value in a before
		// or something.
		if new.Value == 0 {
			me.trackerAnnouncing.Delete(key)
		}
	})
	me.timer.Init(time.Now(), me.timerFunc)
}

// Updates the derived tracker announce head table.
func (me *regularTrackerAnnounceDispatcher) updateTrackerAnnounceHead(url trackerAnnouncerKey) {
	new := me.getTrackerNextAnnounce(url)
	if new.Ok {
		tr := g.OptionFromTuple(me.trackerAnnouncing.Get(url)).Value
		//fmt.Printf("tracker %v has %v announces\n", url, tr)
		me.trackerAnnounceHead.CreateOrReplace(trackerAnnounceHeadRecord{
			trackerRequests:    tr,
			nextAnnounceRecord: new.Unwrap(),
		})
	} else {
		//fmt.Println("looking up", url, "got nothing")
		key := me.trackerAnnounceHead.MinRecord()
		key.url = url
		me.trackerAnnounceHead.Delete(key)
	}
	panicif.NotEq(me.trackerAnnounceHead.Len(), me.nextAnnounce.Len())
	panicif.GreaterThan(me.trackerAnnounceHead.Len(), len(me.trackerClients))
	me.updateTimer()
}

func (me *regularTrackerAnnounceDispatcher) setInfohashActiveCount(key shortInfohash, count int) {
	for k := range me.announceData.IterKeysFrom(torrentTrackerAnnouncerKey{ShortInfohash: key}) {
		if k.ShortInfohash != key {
			break
		}
		me.announceData.Update(k, func(input nextAnnounceInput) nextAnnounceInput {
			input.infohashActive = count
			return input
		})
	}
}

func nextAnnounceRecordFromParts(key torrentTrackerAnnouncerKey, input nextAnnounceInput) nextAnnounceRecord {
	return nextAnnounceRecord{
		torrentTrackerAnnouncerKey: key,
		nextAnnounceInput:          input,
	}
}

func nextAnnounceRecordFromPair(from indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]) nextAnnounceRecord {
	return nextAnnounceRecordFromParts(from.Left, from.Right)
}

func announceIndexCompare(a, b nextAnnounceRecord) int {
	return cmp.Or(
		cmp.Compare(a.url, b.url),
		compareNextAnnounce(a.nextAnnounceInput, b.nextAnnounceInput),
		a.ShortInfohash.Compare(b.ShortInfohash),
	)
}

type infohashConcurrency struct {
	count int
}

// Picks the best announce for a given tracker, and applies filters from announce concurrency limits.
func (me *regularTrackerAnnounceDispatcher) getTrackerNextAnnounce(key trackerAnnouncerKey) (_ g.Option[nextAnnounceRecord]) {
	panicif.NotEq(me.announceIndex.Len(), me.announceData.Len())
	gte := me.announceIndex.MinRecord()
	gte.url = key
	return indexed.IterClusteredWhere(me.announceIndex, gte, func(r nextAnnounceRecord) bool {
		return r.url == key
	}).First()
}

var nextAnnounceRecordCols = []any{
	"Tracker",
	"ShortInfohash",
	"active",
	"Overdue",
	"UntilWhen",
	"|ih|",
	"WantPeers",
	"NeedData",
	"Progress",
	"Webseeds",
	"Event",
	"status line",
}

func (me *regularTrackerAnnounceDispatcher) printNextAnnounceRecordTable(
	sw statusWriter,
	table indexed.Index[nextAnnounceRecord],
) {
	tab := sw.tab()
	tab.cols(nextAnnounceRecordCols...)
	tab.row()
	for r := range table.Iter() {
		me.putNextAnnounceRecordCols(tab, r)
		tab.row()
	}
	tab.end()
}

func (me *regularTrackerAnnounceDispatcher) printNextAnnounceTable(
	sw statusWriter,
	table indexed.Index[trackerAnnounceHeadRecord],
) {
	tab := sw.tab()
	tab.cols("#tr")
	tab.cols(nextAnnounceRecordCols...)
	tab.row()
	for r := range table.Iter() {
		tab.cols(r.trackerRequests)
		me.putNextAnnounceRecordCols(tab, r.nextAnnounceRecord)
		tab.row()
	}
	tab.end()
}

func (me *regularTrackerAnnounceDispatcher) putNextAnnounceRecordCols(
	tab *tableWriter,
	r nextAnnounceRecord,
) {
	t := me.torrentClient.torrentsByShortHash[r.ShortInfohash]
	tab.cols(
		r.url,
		r.ShortInfohash,
		r.active,
		r.overdue,
		time.Until(r.When),
		r.infohashActive,
		r.torrent.Value.WantPeers,
		r.torrent.Value.NeedData,
		fmt.Sprintf("%d%%", int(100*t.progressUnitFloat())),
		r.torrent.Value.HasActiveWebseedRequests,
		r.AnnounceEvent,
		regularTrackerScraperStatusLine(t.regularTrackerAnnounceState[r.torrentTrackerAnnouncerKey]),
	)
}

func (me *regularTrackerAnnounceDispatcher) writeStatus(w io.Writer) {
	sw := statusWriter{w: w}
	// TODO: Print active announces
	sw.f("timer next: %v\n", time.Until(me.timer.When()))
	sw.f("Next announces:\n")
	for sw := range indented(sw) {
		me.printNextAnnounceRecordTable(sw, me.announceIndex)
	}
	fmt.Fprintln(sw, "Next announces")
	for sw := range sw.indented() {
		me.printNextAnnounceTable(sw, me.nextAnnounce)
	}
}

// This moves values that have When that have passed, so we compete on other parts of the priority
// if there is more than one pending. This can be done with another index, and have values move back
// the other way to simplify things.
func (me *regularTrackerAnnounceDispatcher) updateOverdue() {
	now := time.Now()
	var start, end nextAnnounceRecord
	start.overdue = true
	start.When = now.Add(1)
	end.When = now.Add(1)
	var last g.Option[torrentTrackerAnnouncerKey]
again:
	for {
		for r := range indexed.FirstInRange(me.overdueIndex, start, end).Iter() {
			// Check we're making progress.
			if last.Ok {
				if last.Value.Compare(r.torrentTrackerAnnouncerKey) == 0 {
					panic(fmt.Sprintf("last key was %#v, current record is %#v, ", last.Value, r))
				}
			}
			last.Set(r.torrentTrackerAnnouncerKey)
			panicif.False(me.announceData.Update(
				r.torrentTrackerAnnouncerKey,
				func(value nextAnnounceInput) nextAnnounceInput {
					// Must use same now as the range, or we can get stuck scanning the same window
					// wondering and not moving things.
					value.overdue = r.When.Compare(now) <= 0
					return value
				}))
			continue again
		}
		break
	}
}

func (me *regularTrackerAnnounceDispatcher) timerFunc() mytimer.TimeValue {
	me.torrentClient.lock()
	ret := me.step()
	me.torrentClient.unlock()
	return ret
}

// The progress method, called by the timer.
func (me *regularTrackerAnnounceDispatcher) step() mytimer.TimeValue {
	me.dispatchAnnounces()
	// We *are* the Sen... Timer.
	return me.nextTimerDelay()
}

func (me *regularTrackerAnnounceDispatcher) addKey(key torrentTrackerAnnouncerKey) {
	if me.announceData.ContainsKey(key) {
		return
	}
	me.announceData.Create(key, nextAnnounceInput{
		torrent:                me.makeTorrentInput(me.torrentFromShortInfohash(key.ShortInfohash)),
		nextAnnounceStateInput: me.makeAnnounceStateInput(key),
		infohashActive:         g.OptionFromTuple(me.infohashAnnouncing.Get(key.ShortInfohash)).Value.count,
	})
	me.updateTimer()
}

func (me *regularTrackerAnnounceDispatcher) torrentFromShortInfohash(short shortInfohash) *Torrent {
	return me.torrentClient.torrentsByShortHash[short]
}

const maxConcurrentAnnouncesPerTracker = 2

// Returns true if an announce was dispatched and should be tried again.
func (me *regularTrackerAnnounceDispatcher) dispatchAnnounces() {
	for {
		next := me.getNextAnnounce()
		if !next.Ok {
			break
		}
		// Check that torrent input synchronization is working. At this point, running in the
		// dispatcher role, everything should be synced.
		panicif.NotEq(
			next.Value.torrent,
			me.makeTorrentInput(me.torrentFromShortInfohash(next.Value.ShortInfohash)))
		if !next.Value.overdue {
			break
		}
		panicif.True(next.Value.When.After(time.Now()))
		panicif.True(next.Value.active)
		me.startAnnounce(next.Value.torrentTrackerAnnouncerKey)
	}
}

func (me *regularTrackerAnnounceDispatcher) startAnnounce(key torrentTrackerAnnouncerKey) {
	next, ok := me.announceData.Get(key)
	panicif.False(ok)
	panicif.False(me.announceData.Update(key, func(r nextAnnounceInput) nextAnnounceInput {
		panicif.True(r.active)
		r.active = true
		return r
	}))
	me.alterInfohashConcurrency(key.ShortInfohash, func(existing int) int {
		return existing + 1
	})
	me.trackerAnnouncing.UpdateOrCreate(key.url, func(i int) int {
		return i + 1
	})
	me.updateTrackerAnnounceHead(key.url)
	go me.singleAnnouncer(key, next.AnnounceEvent)
}

func (me *regularTrackerAnnounceDispatcher) alterInfohashConcurrency(ih shortInfohash, update func(existing int) int) {
	me.infohashAnnouncing.Alter(
		ih,
		func(ic infohashConcurrency, b bool) (infohashConcurrency, bool) {
			ic.count = update(ic.count)
			panicif.LessThan(ic.count, 0)
			return ic, ic.count > 0
		})
}

func (me *regularTrackerAnnounceDispatcher) finishedAnnounce(key torrentTrackerAnnouncerKey) {
	me.alterInfohashConcurrency(key.ShortInfohash, func(existing int) int { return existing - 1 })
	me.announceData.Update(key, func(r nextAnnounceInput) nextAnnounceInput {
		panicif.False(r.active)
		r.active = false
		// Should this be from the updateTorrentInput method?
		r.torrent = me.makeTorrentInput(me.torrentFromShortInfohash(key.ShortInfohash))
		return r
	})
	me.trackerAnnouncing.Update(key.url, func(i int) int {
		return i - 1
	})
	me.updateTimer()
}

func (me *regularTrackerAnnounceDispatcher) syncAnnounceState(key torrentTrackerAnnouncerKey) {
	input := me.makeAnnounceStateInput(key)
	me.announceData.UpdateOrCreate(key, func(old nextAnnounceInput) nextAnnounceInput {
		old.nextAnnounceStateInput = input
		return old
	})
}

func (me *regularTrackerAnnounceDispatcher) updateTorrentInput(t *Torrent) {
	input := me.makeTorrentInput(t)
	for key := range t.regularTrackerAnnounceState {
		panicif.Zero(key.url)
		panicif.Zero(key.ShortInfohash)
		// Avoid clobbering derived and unrelated values (overdue and active).
		exists := me.announceData.Update(
			key,
			func(av nextAnnounceInput) nextAnnounceInput {
				av.torrent = input
				return av
			},
		)
		panicif.False(exists)
	}
	me.updateTimer()
}

func (me *regularTrackerAnnounceDispatcher) nextTimerDelay() mytimer.TimeValue {
	next := me.getNextAnnounce()
	return next.Value.When
}

func (me *regularTrackerAnnounceDispatcher) updateTimer() {
	me.timer.Update(me.nextTimerDelay())
}

func (me *regularTrackerAnnounceDispatcher) singleAnnouncer(key torrentTrackerAnnouncerKey, event tracker.AnnounceEvent) {
	me.torrentClient.lock()
	defer me.torrentClient.unlock()
	defer me.finishedAnnounce(key)
	ih := key.ShortInfohash
	t := me.torrentClient.torrentsByShortHash[ih]
	req := t.announceRequest(event, ih)
	ctx, cancel := context.WithTimeout(t.closedCtx, tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	// A logger that includes the nice torrent group so we know what the announce is for.
	logger := me.logger.With(
		t.slogGroup(),
		"short infohash", ih,
		"url", key.url,
	)
	me.torrentClient.unlock()
	logger.Debug("announcing", "req", req)
	resp, err := me.trackerClients[key.url].client.Announce(ctx, req, me.getAnnounceOpts())
	now := time.Now()
	{
		level := slog.LevelDebug
		if err != nil {
			if ctx.Err() == nil {
				level = slog.LevelWarn
			}
			level = analog.SlogErrorLevel(err).UnwrapOr(level)
		}
		// numPeers is (.resp.Peers | length) with jq...
		logger.Log(context.Background(), level, "announced", "resp", resp, "err", err)
	}

	me.torrentClient.lock()
	me.updateAnnounceState(key, t, func(state *announceState) {
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
	t.addPeers(peerInfos(nil).AppendFromTracker(resp.Peers))
}

// Updates the announce state, shared by regularTrackerAnnounceDispatcher and Torrent, but it lives in Torrent
// for now.
func (me *regularTrackerAnnounceDispatcher) updateAnnounceState(
	key torrentTrackerAnnouncerKey,
	t *Torrent,
	update func(state *announceState),
) {
	as := t.regularTrackerAnnounceState[key]
	update(&as)
	g.MakeMapIfNil(&t.regularTrackerAnnounceState)
	t.regularTrackerAnnounceState[key] = as
	me.syncAnnounceState(key)
}

func (me *regularTrackerAnnounceDispatcher) getAnnounceOpts() trHttp.AnnounceOpt {
	cfg := me.torrentClient.config
	return trHttp.AnnounceOpt{
		UserAgent: cfg.HTTPUserAgent,
		// TODO: Bring this back.
		//HostHeader:          me.urlHost,
		ClientIp4:           cfg.PublicIp4,
		ClientIp6:           cfg.PublicIp6,
		HttpRequestDirector: cfg.HttpRequestDirector,
	}
}

// Picks the most eligible announce then filters it if it's not allowed.
func (me *regularTrackerAnnounceDispatcher) getNextAnnounce() (_ g.Option[nextAnnounceRecord]) {
	me.updateOverdue()
	v, ok := me.nextAnnounce.GetFirst()
	ok = ok && !v.active && v.trackerRequests < maxConcurrentAnnouncesPerTracker
	return g.OptionFromTuple(v.nextAnnounceRecord, ok)
}

func (me *regularTrackerAnnounceDispatcher) makeAnnounceStateInput(key torrentTrackerAnnouncerKey) nextAnnounceStateInput {
	panicif.Nil(me.torrentClient)
	t := me.torrentClient.torrentsByShortHash[key.ShortInfohash]
	state := t.regularTrackerAnnounceState[key]
	event, when := t.nextAnnounceEvent(key)
	return nextAnnounceStateInput{
		AnnounceEvent:      event,
		When:               when,
		LastAnnounceFailed: state.Err != nil,
	}
}

func (me *regularTrackerAnnounceDispatcher) makeTorrentInput(t *Torrent) (_ g.Option[nextAnnounceTorrentInput]) {
	// No torrent means the client has lost interest and the dispatcher just does followup actions.
	if t == nil {
		return
	}
	return g.Some(nextAnnounceTorrentInput{
		NeedData:                 t.needData(),
		WantPeers:                t.wantPeers(),
		HasActiveWebseedRequests: t.hasActiveWebseedRequests(),
	})
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

func overdueIndexCompare(a, b nextAnnounceRecord) int {
	return cmp.Or(
		compareOverdue(a.nextAnnounceInput, b.nextAnnounceInput),
		a.torrentTrackerAnnouncerKey.Compare(b.torrentTrackerAnnouncerKey),
	)
}

func compareOverdue(a, b nextAnnounceInput) int {
	return cmp.Or(
		-extracmp.CompareBool(a.overdue, b.overdue),
		a.When.Compare(b.When),
	)
}

func compareNextAnnounce(ar, br nextAnnounceInput) (ret int) {
	// What about pushing back based on last announce failure? Some infohashes aren't liked by
	// trackers.
	whenCmp := 0
	// All overdue announces should have the same When. Maybe this can be pushed way back to the end
	// if we have another index for it.
	if !ar.overdue {
		whenCmp = ar.When.Compare(br.When)
	}
	return cmp.Or(
		extracmp.CompareBool(ar.active, br.active),
		-extracmp.CompareBool(ar.overdue, br.overdue),
		whenCmp,
		cmp.Compare(ar.infohashActive, br.infohashActive),
		-extracmp.CompareBool(ar.torrent.Ok, br.torrent.Ok),
		-extracmp.CompareBool(ar.torrent.Value.WantPeers, br.torrent.Value.WantPeers),
		-extracmp.CompareBool(ar.torrent.Value.NeedData, br.torrent.Value.NeedData),
		-extracmp.CompareBool(ar.torrent.Value.HasActiveWebseedRequests, br.torrent.Value.HasActiveWebseedRequests),
		cmp.Compare(eventOrdering[ar.AnnounceEvent], eventOrdering[br.AnnounceEvent]),
	)
}

type nextAnnounceRecord struct {
	torrentTrackerAnnouncerKey
	nextAnnounceInput
}

type nextAnnounceInput struct {
	torrent g.Option[nextAnnounceTorrentInput]
	nextAnnounceStateInput
	infohashActive int
	overdue        bool
	active         bool
}

type nextAnnounceStateInput struct {
	AnnounceEvent      tracker.AnnounceEvent
	When               time.Time
	LastAnnounceFailed bool
}

type nextAnnounceTorrentInput struct {
	NeedData                 bool
	WantPeers                bool
	HasActiveWebseedRequests bool
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
		// Returning now should be fine as sorting should occur on "overdue" derived value.
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

func (cl *Client) startTrackerAnnouncer(u *url.URL, urlStr trackerAnnouncerKey) {
	cl.regularTrackerAnnounceDispatcher.initTrackerClient(u, urlStr, cl.config, cl.logger)
}

func (me *regularTrackerAnnounceDispatcher) initTrackerClient(
	u *url.URL,
	urlStr trackerAnnouncerKey,
	config *ClientConfig,
	logger analog.Logger,
) {
	panicif.NotEq(u.String(), string(urlStr))
	if g.MapContains(me.trackerClients, urlStr) {
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
			Proxy:       config.HTTPProxy,
			DialContext: config.TrackerDialContext,
			ServerName:  u.Hostname(),
		},
		UdpNetwork:   u.Scheme,
		Logger:       logger.WithContextValue(fmt.Sprintf("tracker client for %q", urlStr)),
		ListenPacket: config.TrackerListenPacket,
	})
	panicif.Err(err)
	// Need deep copy
	panicif.NotNil(u.User)
	//ta := &regularTrackerAnnounceDispatcher{
	//	trackerClient: tc,
	//	torrentClient: cl,
	//	urlStr:        urlStr,
	//	urlHost:       u.Host,
	//	logger:        cl.slogger.With("tracker", u.String()),
	//}
	value := trackerClientsValue{
		client: tc,
	}

	g.MakeMapIfNil(&me.trackerClients)
	// TODO: Put the urlHost from here.
	g.MapMustAssignNew(me.trackerClients, urlStr, &value)
}
