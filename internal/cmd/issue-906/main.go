package main

import (
	"log"
	"net/http"
	"os"

	_ "github.com/anacrolix/envpprof"

	"github.com/anacrolix/torrent"
)

func main() {
	cfg := torrent.NewDefaultClientConfig()
	cfg.Debug = true
	cfg.DisablePEX = true
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true

	c, _ := torrent.NewClient(cfg)
	defer c.Close()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c.WriteStatus(w)
	})
	torrentFile := os.Args[1]
	peerAddr := os.Args[2]
	t, _ := c.AddTorrentFromFile(torrentFile)
	println(t.AddPeers([]torrent.PeerInfo{{Addr: addr(peerAddr)}}))
	<-t.GotInfo()
	t.DownloadAll()
	c.WaitAll()
	log.Print("ermahgerd, torrent downloaded")
}

type addr string

func (a addr) String() string {
	return string(a)
}
