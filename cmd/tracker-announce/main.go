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
)

func argSpec(arg string) (ts *torrent.TorrentSpec, err error) {
	if strings.HasPrefix(arg, "magnet:") {
		return torrent.TorrentSpecFromMagnetUri(arg)
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
	var exitCode int32
	var wg sync.WaitGroup
	for _, arg := range flags.Torrents {
		ts, err := argSpec(arg)
		if err != nil {
			log.Fatal(err)
		}
		for _, tier := range ts.Trackers {
			for _, tURI := range tier {
				ar := tracker.AnnounceRequest{
					NumWant:  -1,
					Left:     -1,
					Port:     flags.Port,
					InfoHash: ts.InfoHash,
				}
				wg.Add(1)
				go func(tURI string) {
					defer wg.Done()
					if doTracker(tURI, ar) {
						atomic.StoreInt32(&exitCode, 1)
					}
				}(tURI)
			}
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
		fmt.Printf("response from %q: %+v\n", tURI, resp)
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
