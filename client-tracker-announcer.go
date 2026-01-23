package torrent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"
	"weak"

	g "github.com/anacrolix/generics"
	analog "github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/amortize"
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
	torrentClient *Client
	logger        *slog.Logger
	slow          amortize.Value

	trackerClients map[trackerAnnouncerKey]*trackerClientsValue
	announceStates map[torrentTrackerAnnouncerKey]*announceState
	// Save torrents so we can fetch announce request fields even when the torrent Client has
	// dropped it. We should just prefer to remember the fields we need. Ideally this would map all
	// short infohash forms to the same value. We're using weak.Pointer because we need to clean it
	// up at some point, if this crashes I know to fix it.
	torrentForAnnounceRequests map[shortInfohash]weak.Pointer[Torrent]

	// Raw announce data keyed by announcer and short infohash.
	announceData indexed.Map[torrentTrackerAnnouncerKey, nextAnnounceInput]
	// Announcing sorted by url then priority.
	announceIndex indexed.Index[nextAnnounceRecord]
	// overdue, when, url, ih
	overdueIndex indexed.Index[nextAnnounceRecord]

	// url -> remaining next announce record + tracker (url) request concurrency
	trackerAnnounceHead indexed.Table[trackerAnnounceHeadRecord]
	// trackerAnnounceHead sorted by tracker requests, announce input, key...
	nextAnnounce indexed.Index[trackerAnnounceHeadRecord]

	infohashAnnouncing indexed.Map[shortInfohash, infohashConcurrency]
	trackerAnnouncing  indexed.Map[trackerAnnouncerKey, int]

	timer                      mytimer.Timer
	pendingTorrentInputUpdates map[*Torrent]struct{}
}

type announceDataRow = indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]

type trackerAnnounceHeadRecord struct {
	trackerRequests int // Count of active concurrent requests to a given tracker.
	nextAnnounceRecord
}

type trackerClientsValue struct {
	client tracker.Client
	//active int
}

// According to compareNextAnnounce, which is universal, and we only need to handle the non-zero
// value fields.
func nextAnnounceMinRecord() (ret nextAnnounceRecord) {
	ret.nextAnnounceInput = nextAnnounceInputMin()
	return
}

func nextAnnounceInputMin() (ret nextAnnounceInput) {
	ret.overdue = true
	ret.torrent.Ok = true
	ret.torrent.Value.NeedData = true
	ret.torrent.Value.WantPeers = true
	ret.AnnounceEvent = tracker.Started
	return
}

func (me *regularTrackerAnnounceDispatcher) init(client *Client) {
	me.torrentClient = client
	me.logger = client.slogger
	me.initTables()
	g.MakeMap(&me.pendingTorrentInputUpdates)
	me.initTimer()
}

func (me *regularTrackerAnnounceDispatcher) initTables() {
	me.announceData.Init(torrentTrackerAnnouncerKey.Compare)
	me.announceData.SetMinRecord(torrentTrackerAnnouncerKey{})
	me.announceData.AddInsteadOf(
		func(old, new g.Option[announceDataRow]) g.Option[announceDataRow] {
			if new.Ok {
				new.Value.Right.overdue = new.Value.Right.When.Compare(time.Now()) <= 0
			}
			return new
		})
	// These are logic checks, and so are the first registered triggers.
	me.announceData.OnValueChange(func(key torrentTrackerAnnouncerKey, old, new g.Option[nextAnnounceInput]) {
		if !new.Ok {
			return
		}
		// This should be impossible now, we're updating overdue in the insteadOf.
		if new.Value.overdue && new.Value.When.After(time.Now()) && !new.Value.active {
			panic(fmt.Sprint(key, old, new))
		}
		// This is super pedantic, we're checking distinct root tables are synced with each other. In
		// this case there's a trigger in infohashAnnouncing to update all the corresponding infohashes
		// in announceData. Anytime announceData is changed, we check it's still up to date with
		// infohashAnnouncing.

		// Due to trigger chains that result in announceData being updated *for unrelated fields*,
		// the check occurred prematurely while updating announceData. The fix is to update all
		// indexes, then to do triggers. This is massive overkill for this project right now. TODO:
		// This is probably doable now.
		actual := new.Value.infohashActive
		expected := g.OptionFromTuple(me.infohashAnnouncing.Get(key.ShortInfohash)).Value.count
		if actual != expected {
			me.logger.Debug(
				"announceData.infohashActive != infohashAnnouncing.count",
				"key", key,
				"actual", actual,
				"expected", expected)
		}
	})
	me.announceIndex = indexed.NewFullMappedIndex(
		&me.announceData,
		announceIndexCompare,
		nextAnnounceRecordFromPair,
		nextAnnounceMinRecord(),
	)
	me.overdueIndex = indexed.NewFullMappedIndex(
		&me.announceData,
		overdueIndexCompare,
		nextAnnounceRecordFromPair,
		func() (ret nextAnnounceRecord) {
			ret.overdue = true
			return
		}(),
	)
	me.trackerAnnounceHead.Init(func(a, b trackerAnnounceHeadRecord) int {
		return cmp.Compare(a.url, b.url)
	})
	// Just empty url.
	me.trackerAnnounceHead.SetMinRecord(trackerAnnounceHeadRecord{})
	me.nextAnnounce = indexed.NewFullIndex(
		&me.trackerAnnounceHead,
		func(a, b trackerAnnounceHeadRecord) int {
			return cmp.Or(
				cmp.Compare(a.trackerRequests, b.trackerRequests),
				compareNextAnnounce(a.nextAnnounceInput, b.nextAnnounceInput),
				a.torrentTrackerAnnouncerKey.Compare(b.torrentTrackerAnnouncerKey),
			)
		},
		func() (ret trackerAnnounceHeadRecord) {
			ret.nextAnnounceInput = nextAnnounceInputMin()
			return
		}(),
	)
	// After announce index changes (we need the ordering), update the next announce for each
	// tracker url.
	me.announceData.OnChange(func(old, new g.Option[indexed.Pair[torrentTrackerAnnouncerKey, nextAnnounceInput]]) {
		if old.Ok {
			me.updateTrackerAnnounceHead(old.Value.Left.url)
		}
		if new.Ok && (!old.Ok || old.Value.Left.url != new.Value.Left.url) {
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
			).Exists)
		}
	})
	me.trackerAnnouncing.Init(cmp.Compare)
	me.trackerAnnouncing.AddInsteadOf(func(old, new g.Option[indexed.Pair[trackerAnnouncerKey, int]]) g.Option[indexed.Pair[trackerAnnouncerKey, int]] {
		if new.UnwrapOrZeroValue().Right == 0 {
			new.SetNone()
		}
		return new
	})
	me.trackerAnnouncing.OnValueChange(func(key trackerAnnouncerKey, old, new g.Option[int]) {
		panicif.GreaterThan(new.Value, maxConcurrentAnnouncesPerTracker)
		me.updateTrackerAnnounceHead(key)
	})
}

func (me *regularTrackerAnnounceDispatcher) initTimer() {
	me.timer.Init(mytimer.Immediate(), me.timerFunc)
}

func (me *regularTrackerAnnounceDispatcher) initTimerNoop() {
	me.timer.Init(mytimer.Immediate(), func() mytimer.TimeValue {
		return mytimer.Never()
	})
}

// Updates the derived tracker announce head table.
func (me *regularTrackerAnnounceDispatcher) updateTrackerAnnounceHead(url trackerAnnouncerKey) {
	new := me.getTrackerNextAnnounce(url)
	if new.Ok {
		tr := g.OptionFromTuple(me.trackerAnnouncing.Get(url)).Value
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

// Picks the best announce with a deadline for a given tracker.
func (me *regularTrackerAnnounceDispatcher) getTrackerNextAnnounce(key trackerAnnouncerKey) (ret g.Option[nextAnnounceRecord]) {
	panicif.NotEq(me.announceIndex.Len(), me.announceData.Len())
	gte := me.announceIndex.MinRecord()
	gte.url = key
	ret = me.announceIndex.GetGte(gte)
	if !ret.Ok {
		return
	}
	if ret.Value.active || ret.Value.url != key {
		ret.SetNone()
	}
	return
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
	for r := range table.Iter {
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
	for r := range table.Iter {
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
	t := me.torrentFromShortInfohash(r.ShortInfohash)
	progress := "dropped"
	if t != nil {
		if t.haveInfo() {
			progress = fmt.Sprintf("%d%%", int(100*t.progressUnitFloat()))
		} else {
			progress = "noinfo"
		}
	}
	tab.cols(
		r.url,
		r.ShortInfohash,
		r.active,
		r.overdue,
		time.Until(r.When),
		r.infohashActive,
		r.torrent.Value.WantPeers,
		r.torrent.Value.NeedData,
		progress,
		r.torrent.Value.HasActiveWebseedRequests,
		r.AnnounceEvent,
		regularTrackerScraperStatusLine(*me.announceStates[r.torrentTrackerAnnouncerKey]),
	)
}

func (me *regularTrackerAnnounceDispatcher) writeStatus(w io.Writer) {
	sw := statusWriter{w: w}
	// TODO: Print active announces
	sw.f("timer next: %v\n", time.Until(me.timer.When().Time))
	sw.f("Next announces:\n")
	for sw := range indented(sw) {
		me.printNextAnnounceRecordTable(sw, me.announceIndex)
	}
	fmt.Fprintln(sw, "Next announces")
	for sw := range sw.indented() {
		me.printNextAnnounceTable(sw, me.nextAnnounce)
	}
}

func (me *regularTrackerAnnounceDispatcher) dumpOverdueIndex(now time.Time) {
	for r := range me.overdueIndex.Iter {
		key := r.torrentTrackerAnnouncerKey
		fmt.Println(key, r.overdue, r.When, now)
	}
}

// This moves values that have When that have passed, so we compete on other parts of the priority
// if there is more than one pending. This can be done with another index, and have values move back
// the other way to simplify things.
func (me *regularTrackerAnnounceDispatcher) updateOverdue() {
	now := time.Now()
	start := me.overdueIndex.MinRecord()
	start.When = now.Add(1)
	end := me.overdueIndex.MinRecord()
	end.overdue = false
	end.When = now.Add(1)

	// This stops recursive use while we pivot on a fixed now. We commit to the full update key
	// set now.
	var updateKeys []torrentTrackerAnnouncerKey
	for r := range indexed.IterRange(me.overdueIndex, start, end) {
		updateKeys = append(updateKeys, r.torrentTrackerAnnouncerKey)
	}
	for _, key := range updateKeys {
		// There's no guarantee we actually change anything, the overdue might remain the same due
		// to timing.
		panicif.False(me.announceData.Update(
			key,
			func(value nextAnnounceInput) nextAnnounceInput {
				// Let the insteadOf trigger update overdue for.
				return value
			},
		).Exists)
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
	if len(me.pendingTorrentInputUpdates) != 0 {
		started := time.Now()
		inputs := len(me.pendingTorrentInputUpdates)
		for t := range me.pendingTorrentInputUpdates {
			me.updateTorrentInput(t)
			delete(me.pendingTorrentInputUpdates, t)
		}
		since := time.Since(started)
		if since >= 20*time.Millisecond && me.slow.Try() {
			me.logger.Warn("updating torrent inputs was slow", "took", since, "torrents", inputs)
		}
	}
	me.dispatchAnnounces()
	// We *are* the Sen... Timer.
	return me.nextTimerDelay()
}

func (me *regularTrackerAnnounceDispatcher) addKey(key torrentTrackerAnnouncerKey) {
	if me.announceData.ContainsKey(key) {
		return
	}
	t := me.torrentFromShortInfohash(key.ShortInfohash)
	if t == nil {
		// Crude, but the torrent was already dropped. We probably called AddTrackers late.
		return
	}
	g.MakeMapIfNil(&me.torrentForAnnounceRequests)
	// This can be duplicated when there's multiple trackers for a short infohash. That's fine.
	me.torrentForAnnounceRequests[key.ShortInfohash] = weak.Make(t)
	if !g.MapContains(me.announceStates, key) {
		g.MakeMapIfNil(&me.announceStates)
		g.MapMustAssignNew(me.announceStates, key, g.PtrTo(announceState{}))
	}
	t.regularTrackerAnnounceState[key] = g.MapMustGet(me.announceStates, key)
	me.announceData.Create(key, nextAnnounceInput{
		torrent:                me.makeTorrentInput(t),
		nextAnnounceStateInput: me.makeAnnounceStateInput(key),
		infohashActive:         g.OptionFromTuple(me.infohashAnnouncing.Get(key.ShortInfohash)).Value.count,
	})
	me.updateTimer()
}

// Returns nil if the torrent was dropped.
func (me *regularTrackerAnnounceDispatcher) torrentFromShortInfohash(short shortInfohash) *Torrent {
	return me.torrentClient.torrentsByShortHash[short]
}

const maxConcurrentAnnouncesPerTracker = 2

// Returns true if an announce was dispatched and should be tried again.
func (me *regularTrackerAnnounceDispatcher) dispatchAnnounces() {
	// Should only need to do this once, and only here: By moving the overdue forward, we ignore
	// "When" for anything that's ready. We also don't block so time shouldn't advance meaningfully
	// while we perform dispatching.
	me.updateOverdue()
	for {
		next := me.getNextAnnounce()
		if !next.Ok {
			break
		}
		t := me.torrentFromShortInfohash(next.Value.ShortInfohash)
		// Check that torrent input synchronization is working. At this point, running in the
		// dispatcher role, everything should be synced. Other state in the announce data index is
		// now the original.
		{
			actual := next.Value.torrent
			expected := me.makeTorrentInput(t)
			if actual != expected {
				me.logger.Warn("announce dispatcher torrent input is not synced",
					"expected", fmt.Sprintf("%#v", expected),
					"actual", fmt.Sprintf("%#v", actual))
			}
		}
		if !next.Value.overdue {
			break
		}
		// Pretty sure active stuff shouldn't even be yielded here. Let's check the original
		// assertions for now.
		now := time.Now()
		if next.Value.active || next.Value.When.After(now) {
			me.dumpOverdueIndex(now)
			panic(fmt.Sprintf("bad next announce (now=%v): %#v", now, next.Value))
		}
		me.startAnnounce(next.Value.torrentTrackerAnnouncerKey)
	}
}

func (me *regularTrackerAnnounceDispatcher) startAnnounce(key torrentTrackerAnnouncerKey) {
	next, ok := me.announceData.Get(key)
	panicif.False(ok)
	panicif.NotEq(
		me.announceData.Update(key, func(r nextAnnounceInput) nextAnnounceInput {
			panicif.True(r.active)
			r.active = true
			return r
		}),
		indexed.UpdateResult{
			Exists:  true,
			Changed: true,
		},
	)
	me.alterInfohashConcurrency(key.ShortInfohash, func(existing int) int {
		return existing + 1
	})
	me.trackerAnnouncing.UpdateOrCreate(key.url, func(i int) int {
		return i + 1
	})
	go me.singleAnnounceAttempter(key, next.AnnounceEvent)
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
		res := me.announceData.Update(
			key,
			func(av nextAnnounceInput) nextAnnounceInput {
				av.torrent = input
				// Because completion event
				av.nextAnnounceStateInput = me.makeAnnounceStateInput(key)
				return av
			},
		)
		panicif.False(res.Exists)
	}
}

func (me *regularTrackerAnnounceDispatcher) nextTimerDelay() mytimer.TimeValue {
	if len(me.pendingTorrentInputUpdates) != 0 {
		return mytimer.Immediate()
	}
	next := me.getNextAnnounce()
	return mytimer.FromTime(next.Value.When)
}

func (me *regularTrackerAnnounceDispatcher) updateTimer() {
	me.timer.Update(me.nextTimerDelay())
}

func (me *regularTrackerAnnounceDispatcher) singleAnnounceAttempter(key torrentTrackerAnnouncerKey, event tracker.AnnounceEvent) {
	me.torrentClient.lock()
	defer me.torrentClient.unlock()
	defer me.finishedAnnounce(key)
	ih := key.ShortInfohash
	logger := me.logger.With(
		"short infohash", ih,
		"url", key.url,
	)
	t := me.getTorrentForAnnounceRequest(key.ShortInfohash)
	if t == nil {
		logger.Debug("skipping announce for GCed torrent")
		me.updateAnnounceState(key, func(state *announceState) {
			state.Err = errors.New("announce skipped: Torrent GCed")
			state.lastAttemptCompleted = time.Now()
		})
		me.updateTimer()
	} else {
		me.singleAnnounce(key, event, logger, t)
	}
}

// Actually do an announce. We know *Torrent is accessible.
func (me *regularTrackerAnnounceDispatcher) singleAnnounce(
	key torrentTrackerAnnouncerKey,
	event tracker.AnnounceEvent,
	logger *slog.Logger,
	t *Torrent,
) {
	// A logger that includes the nice torrent group so we know what the announce is for.
	logger = logger.With(t.slogGroup())
	req := t.announceRequest(event, key.ShortInfohash)
	me.torrentClient.unlock()
	ctx, cancel := context.WithTimeout(context.TODO(), tracker.DefaultTrackerAnnounceTimeout)
	defer cancel()
	logger.Debug("announcing", "req", req)
	resp, err := me.trackerClients[key.url].client.Announce(ctx, req, me.getAnnounceOpts())
	now := time.Now()
	{
		level := slog.LevelDebug
		if err != nil {
			level = analog.SlogErrorLevel(err).UnwrapOr(level)
		}
		// numPeers is (.resp.Peers | length) with jq...
		logger.Log(context.Background(), level, "announced", "resp", resp, "err", err)
	}

	me.torrentClient.lock()
	me.updateAnnounceState(key, func(state *announceState) {
		state.Err = err
		state.lastAttemptCompleted = now
		if err == nil {
			state.lastOk = lastAnnounceOk{
				AnnouncedEvent: req.Event,
				Interval:       time.Duration(resp.Interval) * time.Second,
				NumPeers:       len(resp.Peers),
				Completed:      now,
			}
			if req.Event == tracker.Completed {
				state.sentCompleted = true
			}
		}
	})
	t.addPeers(peerInfos(nil).AppendFromTracker(resp.Peers))
}

// Updates the announce state, shared by regularTrackerAnnounceDispatcher and Torrent, but it lives in Torrent
// for now.
func (me *regularTrackerAnnounceDispatcher) updateAnnounceState(
	key torrentTrackerAnnouncerKey,
	update func(state *announceState),
) {
	// It should always be inserted before an update could occur. It should only be removed by the
	// dispatcher. So it should never be nil here.
	as := me.announceStates[key]
	update(as)
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
	// Next announce sorts first on tracker requests, so we can't starve ourselves here I think.
	v, ok := me.nextAnnounce.GetFirst()
	ok = ok && !v.active && v.trackerRequests < maxConcurrentAnnouncesPerTracker
	return g.OptionFromTuple(v.nextAnnounceRecord, ok)
}

func (me *regularTrackerAnnounceDispatcher) makeAnnounceStateInput(key torrentTrackerAnnouncerKey) nextAnnounceStateInput {
	panicif.Zero(me.torrentClient)
	state := me.announceStates[key]
	event, when := me.nextAnnounceEvent(key)
	return nextAnnounceStateInput{
		AnnounceEvent:      event,
		When:               when,
		LastAnnounceFailed: state.Err != nil,
	}
}

func (me *regularTrackerAnnounceDispatcher) makeTorrentInput(t *Torrent) (_ g.Option[nextAnnounceTorrentInput]) {
	// No torrent means the client has lost interest and the dispatcher just does followup actions.
	// If we drop a torrent, we still end up here but with a torrent that should result in None, so
	// check for that.
	if t == nil || t.isDropped() {
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

func overdueIndexCompare(a, b nextAnnounceRecord) (ret int) {
	ret = compareOverdue(a.nextAnnounceInput, b.nextAnnounceInput)
	if ret != 0 {
		return
	}
	return a.torrentTrackerAnnouncerKey.Compare(b.torrentTrackerAnnouncerKey)
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

	ret = cmp.Or(
		extracmp.CompareBool(ar.active, br.active),
		-extracmp.CompareBool(ar.overdue, br.overdue),
	)
	if ret != 0 {
		return
	}
	panicif.NotEq(ar.overdue, br.overdue)
	overdue := ar.overdue
	whenCmp := ar.When.Compare(br.When)
	if !overdue {
		ret = whenCmp
		if ret != 0 {
			return
		}
	}
	return cmp.Or(
		cmp.Compare(ar.infohashActive, br.infohashActive),
		-extracmp.CompareBool(ar.torrent.Ok, br.torrent.Ok),
		-extracmp.CompareBool(ar.torrent.Value.WantPeers, br.torrent.Value.WantPeers),
		-extracmp.CompareBool(ar.torrent.Value.NeedData, br.torrent.Value.NeedData),
		extracmp.CompareBool(ar.torrent.Value.HasActiveWebseedRequests, br.torrent.Value.HasActiveWebseedRequests),
		cmp.Compare(eventOrdering[ar.AnnounceEvent], eventOrdering[br.AnnounceEvent]),
		// Sort on when again, to order amongst announces with the same priorities. Not sure if we
		// want this. Might be masking or fixing a bug in overdue handling.
		whenCmp,
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

// when.IsZero if there's nothing to do and the data can be forgotten.
func (me *regularTrackerAnnounceDispatcher) nextAnnounceEvent(key torrentTrackerAnnouncerKey) (event tracker.AnnounceEvent, when time.Time) {
	state := g.MapMustGet(me.announceStates, key)
	lastOk := state.lastOk
	t := me.torrentFromShortInfohash(key.ShortInfohash)
	if t == nil {
		// Our lastOk attempt was an error.
		if state.Err != nil {
			return
		}
		// We've never announced
		if lastOk.Completed.IsZero() {
			return
		}
		// We already left
		if lastOk.AnnouncedEvent == tracker.Stopped {
			return
		}
		return tracker.Stopped, time.Now()
	}
	// Extend `when` if there was an error on the lastOk attempt. Not required for Stopped because
	// that gives up on error anyway.
	defer func() {
		if state.Err == nil || when.IsZero() {
			return
		}
		minWhen := state.lastAttemptCompleted.Add(time.Minute)
		if when.Before(minWhen) {
			when = minWhen
		}
	}()
	if !state.sentCompleted && t.sawInitiallyIncompleteData && t.haveAllPieces() {
		return tracker.Completed, time.Now()
	}
	if lastOk.Completed.IsZero() {
		// Returning now should be fine as sorting should occur on "overdue" derived value.
		return tracker.Started, time.Now()
	}
	// TODO: Shorten and modify intervals here. Check for completion/stopped etc.
	return tracker.None, lastOk.Completed.Add(lastOk.Interval)
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
	// Has ever sent completed event. Should only be sent once.
	sentCompleted bool
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

// Returns nil if the Torrent has been GCd. Use this lazily as a way to stop caring about announcing
// something, if we don't get to sending Completed or error in time.
func (me *regularTrackerAnnounceDispatcher) getTorrentForAnnounceRequest(ih shortInfohash) *Torrent {
	return g.MapMustGet(me.torrentForAnnounceRequests, ih).Value()
}

func (me *regularTrackerAnnounceDispatcher) pendTorrentInputUpdate(t *Torrent) {
	me.pendingTorrentInputUpdates[t] = struct{}{}
}
