package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

func server() {
	go func() {
		for range time.Tick(time.Second * 5) {
			for _, torrent := range client.Torrents() {
				if torrent.Complete().Bool() {
					fmt.Println("Dropping torrent", torrent.InfoHash().HexString())
					torrent.Drop()
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/torrent", func(w http.ResponseWriter, r *http.Request) {
		if index >= len(infoHashes) {
			w.Write([]byte("No more torrents to add"))
			return
		}

		infoHash := infoHashes[index]
		fmt.Println("Adding torrent", infoHash)

		t, _ := client.AddTorrentInfoHash(metainfo.NewHashFromHex(infoHash))
		go func() {
			<-t.GotInfo()
			fmt.Println("Download started for", infoHash)
			t.DownloadAll()
		}()
		index++

		w.Write([]byte("OK"))
		return
	})

	if err := http.ListenAndServe(":8080", mux); err != nil {
		panic(err)
	}
}
