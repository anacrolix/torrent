package main

import (
	"flag"
	"log"
	"net"
	"os"

	"bitbucket.org/anacrolix/go.torrent/dht"
)

type pingResponse struct {
	addr string
	krpc dht.Msg
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
	pingStrAddrs := flag.Args()
	if len(pingStrAddrs) == 0 {
		os.Stderr.WriteString("u must specify addrs of nodes to ping e.g. router.bittorrent.com:6881\n")
		os.Exit(2)
	}
	s, err := dht.NewServer(nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dht server on %s", s.LocalAddr())
	pingResponses := make(chan pingResponse)
	for _, netloc := range pingStrAddrs {
		addr, err := net.ResolveUDPAddr("udp4", netloc)
		if err != nil {
			log.Fatal(err)
		}
		t, err := s.Ping(addr)
		if err != nil {
			log.Fatal(err)
		}
		go func(addr string) {
			pingResponses <- pingResponse{
				addr: addr,
				krpc: <-t.Response,
			}
		}(netloc)
	}
	for _ = range pingStrAddrs {
		log.Printf("%q", <-pingResponses)
	}
}
