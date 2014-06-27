package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	metainfo "github.com/nsf/libtorgo/torrent"

	"bitbucket.org/anacrolix/go.torrent"
)

var (
	downloadDir = flag.String("downloadDir", "", "directory to store download torrent data")
	testPeer    = flag.String("testPeer", "", "bootstrap peer address")
	httpAddr    = flag.String("httpAddr", "", "http serve address")
	// TODO: Check the default torrent listen port.
	listenAddr      = flag.String("listenAddr", ":6882", "incoming connection address")
	disableTrackers = flag.Bool("disableTrackers", false, "disable trackers")
	seed            = flag.Bool("seed", false, "seed after downloading")
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
}

func makeListener() net.Listener {
	l, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	return l
}

func main() {
	if *httpAddr != "" {
		go http.ListenAndServe(*httpAddr, nil)
	}
	client := torrent.Client{
		DataDir:         *downloadDir,
		Listener:        makeListener(),
		DisableTrackers: *disableTrackers,
	}
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	client.Start()
	defer client.Stop()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "no torrents specified")
		return
	}
	for _, arg := range flag.Args() {
		var ih torrent.InfoHash
		if strings.HasPrefix(arg, "magnet:") {
			m, err := torrent.ParseMagnetURI(arg)
			if err != nil {
				log.Fatalf("error parsing magnet uri: %s", err)
			}
			ih = m.InfoHash
			err = client.AddMagnet(arg)
			if err != nil {
				log.Fatalf("error adding magnet: %s", err)
			}
		} else {
			metaInfo, err := metainfo.LoadFromFile(arg)
			if err != nil {
				log.Fatal(err)
			}
			err = client.AddTorrent(metaInfo)
			if err != nil {
				log.Fatal(err)
			}
			ih = torrent.BytesInfoHash(metaInfo.InfoHash)
		}
		client.PrioritizeDataRegion(ih, 0, 999999999)
		err := client.AddPeers(ih, func() []torrent.Peer {
			if *testPeer == "" {
				return nil
			}
			addr, err := net.ResolveTCPAddr("tcp", *testPeer)
			if err != nil {
				log.Fatal(err)
			}
			return []torrent.Peer{{
				IP:   addr.IP,
				Port: addr.Port,
			}}
		}())
		if err != nil {
			log.Fatal(err)
		}
	}
	if *seed {
		select {}
	}
	if client.WaitAll() {
		log.Print("all torrents completed!")
	} else {
		log.Fatal("y u no complete torrents?!")
	}
}
