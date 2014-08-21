package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"time"

	"bitbucket.org/anacrolix/go.torrent/dht"
	"bitbucket.org/anacrolix/go.torrent/util"
	_ "bitbucket.org/anacrolix/go.torrent/util/profile"
)

type pingResponse struct {
	addr string
	krpc dht.Msg
}

var (
	tableFileName = flag.String("tableFile", "", "name of file for storing node info")
	serveAddr     = flag.String("serveAddr", ":0", "local UDP address")
	infoHash      = flag.String("infoHash", "", "torrent infohash")

	s *dht.Server
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
	switch len(*infoHash) {
	case 20:
	case 40:
		_, err := fmt.Sscanf(*infoHash, "%x", infoHash)
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("require 20 byte infohash")
	}
	var err error
	s, err = dht.NewServer(&dht.ServerConfig{
		Addr: *serveAddr,
	})
	if err != nil {
		log.Fatal(err)
	}
	err = loadTable()
	if err != nil {
		log.Fatalf("error loading table: %s", err)
	}
	log.Printf("dht server on %s, ID is %q", s.LocalAddr(), s.IDString())
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
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		s.StopServing()
	}()
}

func main() {
	seen := make(map[util.CompactPeer]struct{})
	for {
		ps, err := s.GetPeers(*infoHash)
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			for sl := range ps.Values {
				for _, p := range sl {
					if _, ok := seen[p]; ok {
						continue
					}
					seen[p] = struct{}{}
					fmt.Println((&net.UDPAddr{
						IP:   p.IP[:],
						Port: int(p.Port),
					}).String())
				}
			}
		}()
		time.Sleep(15 * time.Second)
	}
	if err := saveTable(); err != nil {
		log.Printf("error saving node table: %s", err)
	}
}
