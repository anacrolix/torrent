package main

import (
	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/tracker"
	"flag"
	"fmt"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
)

var (
	downloadDir = flag.String("downloadDir", "", "directory to store download torrent data")
	testPeer    = flag.String("testPeer", "", "bootstrap peer address")
	profAddr    = flag.String("profAddr", "", "http serve address")
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
}

func main() {
	if *profAddr != "" {
		go http.ListenAndServe(*profAddr, nil)
	}
	client := torrent.Client{
		DataDir: *downloadDir,
		// HalfOpenLimit: 2,
	}
	client.Start()
	defer client.Stop()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "no torrents specified")
		return
	}
	for _, arg := range flag.Args() {
		metaInfo, err := metainfo.LoadFromFile(arg)
		if err != nil {
			log.Fatal(err)
		}
		err = client.AddTorrent(metaInfo)
		if err != nil {
			log.Fatal(err)
		}
		err = client.AddPeers(torrent.BytesInfoHash(metaInfo.InfoHash), func() []torrent.Peer {
			if *testPeer == "" {
				return nil
			}
			addr, err := net.ResolveTCPAddr("tcp", *testPeer)
			if err != nil {
				log.Fatal(err)
			}
			return []torrent.Peer{{
				Peer: tracker.Peer{
					IP:   addr.IP,
					Port: addr.Port,
				}}}
		}())
		if err != nil {
			log.Fatal(err)
		}
	}
	client.WaitAll()
}
