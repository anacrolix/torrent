package main

import (
	"bitbucket.org/anacrolix/go.torrent/dht"
	"log"
	"net"
)

type pingResponse struct {
	addr string
	krpc dht.Msg
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	s := dht.Server{}
	var err error
	s.Socket, err = net.ListenPacket("udp4", "")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dht server on %s", s.Socket.LocalAddr())
	go func() {
		err := s.Serve()
		if err != nil {
			log.Fatal(err)
		}
	}()
	pingResponses := make(chan pingResponse)
	pingStrAddrs := []string{
		"router.utorrent.com:6881",
		"router.bittorrent.com:6881",
	}
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
		log.Print(<-pingResponses)
	}
}
