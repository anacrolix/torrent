package torrent

import (
	"cmp"
	"context"
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

func (me *trackerAnnouncer) run() {
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
		req := me.cl.torrentsByShortHash[announce.infoHash].announceRequest(announce.event, announce.infoHash)
		me.unlock()
		me.tc.Announce(context.TODO(), req, tracker.AnnounceOpt{})
		panic("unimplemented: handle announce response")
		me.lock()
	}
}

func (me *trackerAnnouncer) unlock() {
	me.cl.unlock()
}

func (me *trackerAnnouncer) lock() {
	me.cl.lock()
}
