package main

import (
	"bitbucket.org/anacrolix/go.torrent"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
)

var (
	downloadDir = flag.String("downloadDir", "", "directory to store download torrent data")
)

func init() {
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
	}
	client.WaitAll()
	client.Close()
}
