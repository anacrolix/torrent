package main

import (
	"fmt"
	"io"
	"log"

	"github.com/anacrolix/torrent"
)

func main() {
	err := mainErr()
	if err != nil {
		log.Fatalf("error in main: %v", err)
	}
}

func mainErr() error {
	cfg := torrent.NewDefaultClientConfig()
	// We could disable non-webseed peer types here, to force any errors.
	client, _ := torrent.NewClient(cfg)

	// Add directly from metainfo, because we want to force webseeding to serve data, and webseeding
	// won't get us the metainfo.
	t, err := client.AddTorrentFromFile("testdata/The WIRED CD - Rip. Sample. Mash. Share.torrent")
	if err != nil {
		return err
	}
	<-t.GotInfo()

	fmt.Println("GOT INFO")

	f := t.Files()[0]

	r := f.NewReader()

	r.Seek(5, io.SeekStart)
	buf := make([]byte, 5)
	n, err := r.Read(buf)

	fmt.Println("END", n, string(buf), err)

	t.DownloadAll()
	client.WaitAll()
	return nil
}
