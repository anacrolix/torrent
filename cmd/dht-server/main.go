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
	s.Socket, err = net.ListenUDP("udp4", nil)
	if err != nil {
		log.Fatal(err)
	}
	s.Init()
	log.Printf("dht server on %s", s.Socket.LocalAddr())
	go func() {
		err := s.Serve()
		if err != nil {
			log.Fatal(err)
		}
	}()
	err = s.Bootstrap()
	if err != nil {
		log.Fatal(err)
	}
	select {}
}
