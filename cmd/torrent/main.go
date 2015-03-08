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

	_ "github.com/anacrolix/envpprof"

	"github.com/anacrolix/libtorgo/metainfo"

	"bitbucket.org/anacrolix/go.torrent"
)

var (
	downloadDir = flag.String("downloadDir", "", "directory to store download torrent data")
	testPeer    = flag.String("testPeer", "", "bootstrap peer address")
	// TODO: Check the default torrent listen port.
	listenAddr      = flag.String("listenAddr", ":50007", "incoming connection address")
	disableTrackers = flag.Bool("disableTrackers", false, "disable trackers")
	seed            = flag.Bool("seed", false, "seed after downloading")
	upload          = flag.Bool("upload", true, "upload data to peers")
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
	client, err := torrent.NewClient(&torrent.Config{
		DataDir:         *downloadDir,
		DisableTrackers: *disableTrackers,
		ListenAddr:      *listenAddr,
		NoUpload:        !*upload,
	})
	if err != nil {
		log.Fatalf("error creating client: %s", err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	defer client.Close()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "no torrents specified")
		return
	}
	for _, arg := range flag.Args() {
		t := func() torrent.Torrent {
			if strings.HasPrefix(arg, "magnet:") {
				t, err := client.AddMagnet(arg)
				if err != nil {
					log.Fatalf("error adding magnet: %s", err)
				}
				return t
			} else {
				metaInfo, err := metainfo.LoadFromFile(arg)
				if err != nil {
					log.Fatal(err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					log.Fatal(err)
				}
				return t
			}
		}()
		// client.PrioritizeDataRegion(ih, 0, 999999999)
		err := t.AddPeers(func() []torrent.Peer {
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
		go func() {
			<-t.GotMetainfo
			t.DownloadAll()
		}()
	}
	if *seed {
		select {}
	}
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		log.Fatal("y u no complete torrents?!")
	}
}
