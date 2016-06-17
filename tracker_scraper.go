package torrent

import (
	"fmt"
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
	// peers count
	peers int
	// error shown by UI
	err string
	// calls information
	lastAnnounce int64
	nextAnnounce int64
	// update interval set by remote tracker
	interval int32
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

func (me *trackerScraper) Error(str string) string {
	me.t.cl.mu.Lock()
	defer me.t.cl.mu.Unlock()

	me.err = str
	return me.err
}

func (me *trackerScraper) Url() string {
	me.t.cl.mu.Lock()
	defer me.t.cl.mu.Unlock()

	return me.url
}

// Return how long to wait before trying again. For most errors, we return 5
// minutes, a relatively quick turn around for DNS changes.
func (me *trackerScraper) announce() []Peer {
	blocked, urlToUse, host, err := me.t.cl.prepareTrackerAnnounceUnlocked(me.url)
	if err != nil {
		str := me.Error(fmt.Sprintf("error preparing announce to %q: %s", me.Url(), err))
		log.Println(str)
		return nil
	}
	if blocked {
		str := me.Error(fmt.Sprintf("announce to tracker %q blocked by IP", me.Url()))
		log.Println(str)
		return nil
	}

	me.t.cl.mu.Lock()
	me.lastAnnounce = time.Now().Unix()
	req := me.t.announceRequest()
	me.t.cl.mu.Unlock()

	res, err := tracker.AnnounceHost(urlToUse, &req, host)
	if err != nil {
		me.Error(fmt.Sprintf("error announcing %s %q to %q: %s", me.t.InfoHash().HexString(), me.t.Name(), me.Url(), err))
		return nil
	}

	me.t.cl.mu.Lock()
	me.peers = len(res.Peers)
	me.interval = res.Interval
	me.t.cl.mu.Unlock()

	return trackerToTorrentPeers(res.Peers)
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

		peers := me.announce()

		dur := 5 * time.Minute

		me.t.cl.mu.Lock()
		// torrent can already be stopped, ignore incoming data
		if me.stop.IsSet() {
			me.t.cl.mu.Unlock()
			return
		}
		if peers != nil {
			me.t.addPeers(peers)
			dur = time.Duration(me.interval) * time.Second
		}
		me.nextAnnounce = me.lastAnnounce + int64(dur.Seconds())
		me.t.cl.mu.Unlock()

		intervalChan := time.After(dur)

		select {
		case <-me.t.closed.LockedChan(&me.t.cl.mu):
			return
		case <-me.stop.LockedChan(&me.t.cl.mu):
			return
		case <-intervalChan:
		}
	}
}
