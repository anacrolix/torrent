package torrent

import (
	"log"
	"time"

	"github.com/anacrolix/missinggo"

	"github.com/anacrolix/torrent/tracker"
)

// Announces a torrent to a tracker at regular intervals, when peers are
// required.
type trackerScraper struct {
	url string
	// Causes the trackerScraper to stop running.
	stop missinggo.Event
	t    *Torrent
}

func trackerToTorrentPeers(ps []tracker.Peer) (ret []Peer) {
	ret = make([]Peer, 0, len(ps))
	for _, p := range ps {
		ret = append(ret, Peer{
			IP:     p.IP,
			Port:   p.Port,
			Source: peerSourceTracker,
		})
	}
	return
}

// Return how long to wait before trying again. For most errors, we return 5
// minutes, a relatively quick turn around for DNS changes.
func (me *trackerScraper) announce() time.Duration {
	blocked, urlToUse, host, err := me.t.cl.prepareTrackerAnnounceUnlocked(me.url)
	if err != nil {
		log.Printf("error preparing announce to %q: %s", me.url, err)
		return 5 * time.Minute
	}
	if blocked {
		log.Printf("announce to tracker %q blocked by IP", me.url)
		return 5 * time.Minute
	}
	me.t.cl.mu.Lock()
	req := me.t.announceRequest()
	me.t.cl.mu.Unlock()
	res, err := tracker.AnnounceHost(urlToUse, &req, host)
	if err != nil {
		log.Printf("error announcing %s %q to %q: %s", me.t.InfoHash().HexString(), me.t.Name(), me.url, err)
		return 5 * time.Minute
	}
	me.t.AddPeers(trackerToTorrentPeers(res.Peers))
	return time.Duration(res.Interval) * time.Second
}

func (me *trackerScraper) Run() {
	for {
		select {
		case <-me.t.closed.LockedChan(&me.t.cl.mu):
			return
		case <-me.stop.LockedChan(&me.t.cl.mu):
			return
		case <-me.t.wantPeersEvent.LockedChan(&me.t.cl.mu):
		}

		intervalChan := time.After(me.announce())

		select {
		case <-me.t.closed.LockedChan(&me.t.cl.mu):
			return
		case <-me.stop.LockedChan(&me.t.cl.mu):
			return
		case <-intervalChan:
		}
	}
}
