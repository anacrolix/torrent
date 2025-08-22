package torrent

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/tracker"
)

// Tracker URL split for network or other dimensions as required for each announcer.
type clientTrackerKey struct {
	// This can differ from the URL scheme when announcing occurs on different networks.
	Network string
	Url     string
}

// A unique thing to announce. Note that Torrents can have multiple infohashes.
type announceKey struct {
	infoHash [20]byte
}

type trackerAnnouncer struct {
	cancel    context.CancelCauseFunc
	cl        *Client
	tc        tracker.Client
	tUrl      string
	tKey      clientTrackerKey
	logger    *slog.Logger
	sleepCond chansync.BroadcastCond
}

func (me *trackerAnnouncer) announce(t *Torrent) {
}

// Should we consider t for announcing to this tracker? We might want to stop when a tracker is
// removed.
func (me *trackerAnnouncer) announceTorrent(t *Torrent) bool {
	for _, tier := range t.announceList {
		for _, url := range tier {
			if url == me.tUrl {
				return true
			}
		}
	}
	return false
}

func (me *trackerAnnouncer) getNextAnnounces() (ret []trackerNextAnnounce) {
	for h, t := range me.cl.torrentsByShortHash {
		tna := t.nextAnnounce(trackerAnnounceKey{
			announceKey: announceKey{
				infoHash: h,
			},
			clientTrackerKey: me.tKey,
		})
		if !tna.Ok {
			continue
		}
		ret = append(ret, trackerNextAnnounce{
			infoHash:            h,
			torrentNextAnnounce: tna.Value,
		})
	}
	return
}

type trackerNextAnnounce struct {
	infoHash [20]byte
	torrentNextAnnounce
}

func (me *trackerAnnouncer) getNextAnnounce() (_ g.Option[trackerNextAnnounce]) {
	announces := me.getNextAnnounces()
	slices.SortFunc(announces, func(a, b trackerNextAnnounce) int {
		aT := me.cl.torrentsByShortHash[a.infoHash]
		bT := me.cl.torrentsByShortHash[b.infoHash]
		// TODO: Enhance this to incorporate announce event type, whether there are already peers,
		// need data, have announced to other trackers, etc.
		return cmp.Or(
			// I think this has to go first, or we can starve ourselves by sleeping too long.
			a.when.Compare(b.when),
			compareBool(aT.hasActiveWebseedRequests(), bT.hasActiveWebseedRequests()),
			cmp.Compare(announceEventPriority[a.event], announceEventPriority[b.event]),
		)
	})
	if len(announces) == 0 {
		return
	}
	return g.Some(announces[0])
}

// Lowest priority is the one that should be announced first.
var announceEventPriority = map[tracker.AnnounceEvent]int{
	// Need swarm to know we even exist.
	tracker.Started: 1,
	// Prevent more connections.
	tracker.Stopped: 2,
	// This is edge triggered so we want to capture if we can.
	tracker.Completed: 3,
	// Bad luck buddy you're last.
	tracker.None: 4,
}

func (me *trackerAnnouncer) run(ctx context.Context) {
	me.logger.Debug("started running")
	defer me.logger.Debug("stopped running")
	me.lock()
	defer me.unlock()
	for {
		announceOpt := me.getNextAnnounce()
		if !announceOpt.Ok {
			// No more announces to do.
			return
		}
		announce := announceOpt.Value
		if until := time.Until(announce.when); until > 0 {
			sleepCond := me.sleepCond.Signaled()
			me.unlock()
			select {
			case <-sleepCond:
			case <-time.After(until):
			}
			me.lock()
			continue
		}
		t := me.cl.torrentsByShortHash[announce.infoHash]
		req := t.announceRequest(announce.event, announce.infoHash)

		me.unlock()
		res, err := me.tc.Announce(ctx, req, tracker.AnnounceOpt{})
		me.logger.Debug("completed announce",
			"err", err,
			"ih", fmt.Sprintf("%x", announce.infoHash),
			"peers", len(res.Peers),
			"interval", res.Interval,
		)

		newLast := lastTrackerAnnounce{
			event: g.Some(req.Event),
			result: trackerAnnounceResult{
				Err:       err,
				NumPeers:  len(res.Peers),
				Interval:  time.Duration(res.Interval) * time.Second,
				Completed: time.Now(),
			},
		}
		me.lock()
		if ctx.Err() != nil {
			return
		}
		g.MakeMapIfNil(&t.lastTrackerAnnounces)
		t.lastTrackerAnnounces[me.trackerAnnounceKey(announce.infoHash)] = newLast
		t.addPeers(peerInfos(nil).AppendFromTracker(res.Peers))
	}
}

func (me *trackerAnnouncer) trackerAnnounceKey(infoHash [20]byte) trackerAnnounceKey {
	return trackerAnnounceKey{
		announceKey:      announceKey{infoHash: infoHash},
		clientTrackerKey: me.tKey,
	}
}

func (me *trackerAnnouncer) unlock() {
	me.cl.unlock()
}

func (me *trackerAnnouncer) lock() {
	me.cl.lock()
}
