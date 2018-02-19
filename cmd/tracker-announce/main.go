package main

import (
	"log"
	"math"
	"net/url"
	"strings"
	"sync"

	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/tracker"
	"github.com/davecgh/go-spew/spew"
)

func argSpec(arg string) (ts *torrent.TorrentSpec, err error) {
	if strings.HasPrefix(arg, "magnet:") {
		return torrent.TorrentSpecFromMagnetURI(arg)
	}
	mi, err := metainfo.LoadFromFile(arg)
	if err != nil {
		return
	}
	ts = torrent.TorrentSpecFromMetaInfo(mi)
	return
}

func main() {
	flags := struct {
		tagflag.StartPos
		Torrents []string `arity:"+"`
	}{}
	tagflag.Parse(&flags)
	ar := tracker.AnnounceRequest{
		NumWant: -1,
		Left:    math.MaxUint64,
	}
	var wg sync.WaitGroup
	for _, arg := range flags.Torrents {
		ts, err := argSpec(arg)
		if err != nil {
			log.Fatal(err)
		}
		ar.InfoHash = ts.InfoHash
		for _, tier := range ts.Trackers {
			for _, tURI := range tier {
				wg.Add(1)
				go doTracker(tURI, wg.Done)
			}
		}
	}
	wg.Wait()
}

func doTracker(tURI string, done func()) {
	defer done()
	for _, res := range announces(tURI) {
		err := res.error
		resp := res.AnnounceResponse
		if err != nil {
			log.Printf("error announcing to %q: %s", tURI, err)
			continue
		}
		log.Printf("tracker response from %q: %s", tURI, spew.Sdump(resp))
	}
}

type announceResult struct {
	tracker.AnnounceResponse
	error
}

func announces(uri string) (ret []announceResult) {
	u, err := url.Parse(uri)
	if err != nil {
		return []announceResult{{error: err}}
	}
	a := tracker.Announce{
		TrackerUrl: uri,
	}
	if u.Scheme == "udp" {
		a.UdpNetwork = "udp4"
		ret = append(ret, announce(a))
		a.UdpNetwork = "udp6"
		ret = append(ret, announce(a))
		return
	}
	return []announceResult{announce(a)}
}

func announce(a tracker.Announce) announceResult {
	resp, err := a.Do()
	return announceResult{resp, err}
}
