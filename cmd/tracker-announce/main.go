package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/tracker"
	"github.com/davecgh/go-spew/spew"
)

func argSpec(arg string) (ts *torrent.TorrentSpec, _ error) {
	if strings.HasPrefix(arg, "magnet:") {
		return torrent.TorrentSpecFromMagnetUri(arg)
	}
	mi, fileErr := metainfo.LoadFromFile(arg)
	if fileErr == nil {
		ts = torrent.TorrentSpecFromMetaInfo(mi)
		return
	}
	var ih torrent.InfoHash
	ihErr := ih.FromHexString(arg)
	if ihErr == nil {
		ts = &torrent.TorrentSpec{
			InfoHash: ih,
		}
		return
	}
	if len(arg) == 40 {
		return nil, ihErr
	} else {
		return nil, fileErr
	}
}

func main() {
	flags := struct {
		Port    uint16
		Tracker []string
		tagflag.StartPos
		Torrents []string `arity:"+"`
	}{
		Port: 50007,
	}
	tagflag.Parse(&flags)
	var exitCode int32
	var wg sync.WaitGroup
	startAnnounce := func(ih torrent.InfoHash, tURI string) {
		ar := tracker.AnnounceRequest{
			NumWant:  -1,
			Left:     -1,
			Port:     flags.Port,
			InfoHash: ih,
		}
		wg.Add(1)
		go func(tURI string) {
			defer wg.Done()
			if doTracker(tURI, ar) {
				atomic.StoreInt32(&exitCode, 1)
			}
		}(tURI)
	}
	for _, arg := range flags.Torrents {
		ts, err := argSpec(arg)
		if err != nil {
			log.Fatal(err)
		}
		for _, tier := range ts.Trackers {
			for _, tURI := range tier {
				startAnnounce(ts.InfoHash, tURI)
			}
		}
		for _, tUri := range flags.Tracker {
			startAnnounce(ts.InfoHash, tUri)
		}
	}
	wg.Wait()
	os.Exit(int(exitCode))
}

func doTracker(tURI string, ar tracker.AnnounceRequest) (hadError bool) {
	for _, res := range announces(tURI, ar) {
		err := res.error
		resp := res.AnnounceResponse
		if err != nil {
			hadError = true
			log.Printf("error announcing to %q: %s", tURI, err)
			continue
		}
		fmt.Printf("from %q for %x:\n%s", tURI, ar.InfoHash, spew.Sdump(resp))
	}
	return
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
