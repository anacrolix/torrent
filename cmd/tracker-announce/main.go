package main

import (
	"log"
	"net/url"
	"strings"
	"sync"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/tracker"
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
		Port uint16
		tagflag.StartPos
		Torrents []string `arity:"+"`
	}{
		Port: 50007,
	}
	tagflag.Parse(&flags)
	ar := tracker.AnnounceRequest{
		NumWant: -1,
		Left:    -1,
		Port:    flags.Port,
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
				go doTracker(tURI, wg.Done, ar)
			}
		}
	}
	wg.Wait()
}

func doTracker(tURI string, done func(), ar tracker.AnnounceRequest) {
	defer done()
	for _, res := range announces(tURI, ar) {
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

func announces(uri string, ar tracker.AnnounceRequest) (ret []announceResult) {
	u, err := url.Parse(uri)
	if err != nil {
		return []announceResult{{error: err}}
	}
	a := tracker.Announce{
		Request:    ar,
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
