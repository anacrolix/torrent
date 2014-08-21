package main

import (
	"flag"
	"log"
	"os"

	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	_ "bitbucket.org/anacrolix/go.torrent/tracker/udp"
	"bitbucket.org/anacrolix/go.torrent/util"
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
			ar := tracker.AnnounceRequest{
				NumWant: -1,
			}
			util.CopyExact(ar.InfoHash, mi.InfoHash)
			resp, err := tr.Announce(&ar)
			if err != nil {
				log.Fatal(err)
			}
			log.Print(resp)
		}
	}
}
