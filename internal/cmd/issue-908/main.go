package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/davecgh/go-spew/spew"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func main() {
	cfg := torrent.NewDefaultClientConfig()
	cfg.Seed = true
	cfg.Debug = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true

	cl, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%x\n", cl.PeerID())
	defer cl.Close()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cl.WriteStatus(w)
	})
	go http.ListenAndServe(":8080", nil)

	filePath := "testdata"
	fi, err := os.Stat(filePath)
	if err != nil {
		panic(err)
	}
	totalLength := fi.Size()
	if err != nil {
		log.Fatal(err)
	}
	pieceLength := metainfo.ChoosePieceLength(totalLength)
	info := metainfo.Info{
		PieceLength: pieceLength,
	}
	err = info.BuildFromFilePath(filePath)
	if err != nil {
		log.Fatal(err)
	}
	for _, fi := range info.Files {
		log.Printf("added %q", fi.BestPath())
	}
	mi := &metainfo.MetaInfo{
		InfoBytes: bencode.MustMarshal(info),
	}
	spew.Dump(info)
	torrentFile := mi
	torrentFile.Announce = ""

	// Add the torrent to the client
	tor, err := cl.AddTorrent(torrentFile)
	if err != nil {
		log.Fatal(err)
	}

	// Wait for the torrent to be ready
	<-tor.GotInfo()

	hash := tor.InfoHash()
	fmt.Printf("%v\n", tor.Metainfo().Magnet(&hash, tor.Info()))

	// Announce the torrent to DHT
	for _, _ds := range cl.DhtServers() {
		ds := _ds
		done, _, err := tor.AnnounceToDht(ds)
		if err != nil {
			log.Fatal(err)
		}
		for c := range done {
			fmt.Println("++++++++++++++++++++++++", c)
		}
	}
	select {}
}
