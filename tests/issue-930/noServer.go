package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anacrolix/torrent/metainfo"
)

func noServer() {
	fmt.Println("Ctrl+C to start downloading")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	for _, hash := range infoHashes {
		t, _ := client.AddTorrentInfoHash(metainfo.NewHashFromHex(hash))
		infoHash := hash
		go func() {
			<-t.GotInfo()
			fmt.Println("Download started for", infoHash)
			t.DownloadAll()
		}()
	}

	client.WaitAll()
	fmt.Println("All torrents downloaded")

	fmt.Println("Ctrl+C to stop program")
	sc2 := make(chan os.Signal, 1)
	signal.Notify(sc2, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc2
}
