package main

import (
	"bitbucket.org/anacrolix/go.torrent"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
	"net"
)

var (
	downloadDir = flag.String("downloadDir", "", "directory to store download torrent data")
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	flag.Parse()
}

func main() {
	client := torrent.NewClient(*downloadDir)
	for _, arg := range flag.Args() {
		metaInfo, err := metainfo.LoadFromFile(arg)
		if err != nil {
			log.Fatal(err)
		}
		err = client.AddTorrent(metaInfo)
		if err != nil {
			log.Fatal(err)
		}
		err = client.AddPeers(torrent.BytesInfoHash(metaInfo.InfoHash), []torrent.Peer{{
			IP:   net.IPv4(127, 0, 0, 1),
			Port: 53219,
		}})
		if err != nil {
			log.Fatal(err)
		}
	}
	client.WaitAll()
	client.Close()
}
