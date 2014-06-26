package main

import (
	"bitbucket.org/anacrolix/go.torrent/dht"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
)

type pingResponse struct {
	addr string
	krpc dht.Msg
}

var (
	tableFileName = flag.String("tableFile", "", "name of file for storing node info")
	serveAddr     = flag.String("serveAddr", ":0", "local UDP address")

	s dht.Server
)

func loadTable() error {
	if *tableFileName == "" {
		return nil
	}
	f, err := os.Open(*tableFileName)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("error opening table file: %s", err)
	}
	defer f.Close()
	added := 0
	for {
		b := make([]byte, dht.CompactNodeInfoLen)
		_, err := io.ReadFull(f, b)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading table file: %s", err)
		}
		var ni dht.NodeInfo
		err = ni.UnmarshalCompact(b)
		if err != nil {
			return fmt.Errorf("error unmarshaling compact node info: %s", err)
		}
		s.AddNode(ni)
		added++
	}
	log.Printf("loaded %d nodes from table file", added)
	return nil
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
	var err error
	s.Socket, err = net.ListenUDP("udp4", func() *net.UDPAddr {
		addr, err := net.ResolveUDPAddr("udp4", *serveAddr)
		if err != nil {
			log.Fatalf("error resolving serve addr: %s", err)
		}
		return addr
	}())
	if err != nil {
		log.Fatal(err)
	}
	s.Init()
	err = loadTable()
	if err != nil {
		log.Fatalf("error loading table: %s", err)
	}
	log.Printf("dht server on %s, ID is %q", s.Socket.LocalAddr(), s.IDString())
	setupSignals()
}

func saveTable() error {
	goodNodes := s.Nodes()
	if *tableFileName == "" {
		if len(goodNodes) != 0 {
			log.Printf("discarding %d good nodes!", len(goodNodes))
		}
		return nil
	}
	f, err := os.OpenFile(*tableFileName, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return fmt.Errorf("error opening table file: %s", err)
	}
	defer f.Close()
	for _, nodeInfo := range goodNodes {
		var b [dht.CompactNodeInfoLen]byte
		err := nodeInfo.PutCompact(b[:])
		if err != nil {
			return fmt.Errorf("error compacting node info: %s", err)
		}
		_, err = f.Write(b[:])
		if err != nil {
			return fmt.Errorf("error writing compact node info: %s", err)
		}
	}
	log.Printf("saved %d nodes to table file", len(goodNodes))
	return nil
}

func setupSignals() {
	ch := make(chan os.Signal)
	signal.Notify(ch)
	go func() {
		<-ch
		s.StopServing()
	}()
}

func main() {
	go func() {
		err := s.Bootstrap()
		if err != nil {
			log.Printf("error bootstrapping: %s", err)
			s.StopServing()
		} else {
			log.Print("bootstrapping complete")
		}
	}()
	err := s.Serve()
	if err := saveTable(); err != nil {
		log.Printf("error saving node table: %s", err)
	}
	if err != nil {
		log.Fatalf("error serving dht: %s", err)
	}
}
