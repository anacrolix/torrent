package main

import (
	"flag"
	"log"
	"os"

	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
	metainfo "github.com/nsf/libtorgo/torrent"
)

func main() {
	flag.Parse()
	mi, err := metainfo.Load(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	for _, tier := range mi.AnnounceList {
		for _, url := range tier {
			tr, err := tracker.New(url)
			if err != nil {
				log.Fatal(err)
			}
			err = tr.Connect()
			if err != nil {
				log.Fatal(err)
			}
			resp, err := tr.Announce(&tracker.AnnounceRequest{
				NumWant:  -1,
				InfoHash: torrent.BytesInfoHash(mi.InfoHash),
			})
			if err != nil {
				log.Fatal(err)
			}
			log.Print(resp)
		}
	}
}
