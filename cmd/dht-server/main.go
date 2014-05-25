package main

import (
	"bitbucket.org/anacrolix/go.torrent/dht"
	"log"
	"net"
	"os"
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
	func() {
		f, err := os.Open("nodes")
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		err = s.ReadNodes(f)
		if err != nil {
			log.Fatal(err)
		}
	}()
	log.Printf("dht server on %s", s.Socket.LocalAddr())
	go func() {
		err := s.Serve()
		if err != nil {
			log.Fatal(err)
		}
	}()
	err = s.Bootstrap()
	func() {
		f, err := os.OpenFile("nodes", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
		if err != nil {
			log.Print(err)
			return
		}
		defer f.Close()
		s.WriteNodes(f)
	}()
	if err != nil {
		log.Fatal(err)
	}
}
