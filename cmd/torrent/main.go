package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	"github.com/jessevdk/go-flags"

	_ "github.com/anacrolix/envpprof"

	"github.com/anacrolix/libtorgo/metainfo"

	"github.com/anacrolix/torrent"
)

// fmt.Fprintf(os.Stderr, "Usage: %s \n", os.Args[0])

func resolvedPeerAddrs(ss []string) (ret []torrent.Peer, err error) {
	for _, s := range ss {
		var addr *net.TCPAddr
		addr, err = net.ResolveTCPAddr("tcp", s)
		if err != nil {
			return
		}
		ret = append(ret, torrent.Peer{
			IP:   addr.IP,
			Port: addr.Port,
		})
	}
	return
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	var rootGroup struct {
		Client    torrent.Config `group:"Client Options"`
		Seed      bool           `long:"seed" description:"continue seeding torrents after completed"`
		TestPeers []string       `long:"test-peer" description:"address of peer to inject to every torrent"`
	}
	// Don't pass flags.PrintError because it's inconsistent with printing.
	// https://github.com/jessevdk/go-flags/issues/132
	parser := flags.NewParser(&rootGroup, flags.HelpFlag|flags.PassDoubleDash)
	parser.Usage = "[OPTIONS] (magnet URI or .torrent file path)..."
	posArgs, err := parser.Parse()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Download from the BitTorrent network.\n")
		fmt.Println(err)
		os.Exit(2)
	}
	testPeers, err := resolvedPeerAddrs(rootGroup.TestPeers)
	if err != nil {
		log.Fatal(err)
	}

	if len(posArgs) == 0 {
		fmt.Fprintln(os.Stderr, "no torrents specified")
		return
	}
	client, err := torrent.NewClient(&rootGroup.Client)
	if err != nil {
		log.Fatalf("error creating client: %s", err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	defer client.Close()
	for _, arg := range posArgs {
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
		err := t.AddPeers(testPeers)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			<-t.GotMetainfo
			t.DownloadAll()
		}()
	}
	if rootGroup.Seed {
		// We never finish, since we intend to seed indefinitely.
		select {}
	}
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		log.Fatal("y u no complete torrents?!")
	}
}
