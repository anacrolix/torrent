// Converts magnet URIs and info hashes into torrent metainfo files.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
)

func main() {
	args := struct {
		tagflag.StartPos
		Magnet []string
	}{}
	tagflag.Parse(&args)
	cl, err := torrent.NewClient(nil)
	if err != nil {
		log.Fatalf("error creating client: %s", err)
	}
	http.HandleFunc("/torrent", func(w http.ResponseWriter, r *http.Request) {
		cl.WriteStatus(w)
	})
	http.HandleFunc("/dht", func(w http.ResponseWriter, r *http.Request) {
		for _, ds := range cl.DhtServers() {
			ds.WriteStatus(w)
		}
	})
	wg := sync.WaitGroup{}
	for _, arg := range args.Magnet {
		t, err := cl.AddMagnet(arg)
		if err != nil {
			log.Fatalf("error adding magnet to client: %s", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-t.GotInfo()
			mi := t.Metainfo()
			t.Drop()
			f, err := os.Create(metainfoFileName(t.Info().BestName(), t.InfoHash().HexString()))
			if err != nil {
				log.Fatalf("error creating torrent metainfo file: %s", err)
			}
			defer f.Close()
			err = bencode.NewEncoder(f).Encode(mi)
			if err != nil {
				log.Fatalf("error writing torrent metainfo file: %s", err)
			}
		}()
	}
	wg.Wait()
}

// The file name to write a torrent's metainfo to in the current directory. The info name comes from
// the torrent, so it's only used if it's a plain file name. Otherwise the infohash is used.
func metainfoFileName(infoName, infoHashHex string) string {
	if infoName == "" ||
		infoName == "." ||
		strings.ContainsAny(infoName, `/\`) ||
		!filepath.IsLocal(infoName) {
		infoName = infoHashHex
	}
	return infoName + ".torrent"
}
