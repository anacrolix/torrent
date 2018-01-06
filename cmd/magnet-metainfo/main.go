// Converts magnet URIs and info hashes into torrent metainfo files.
package main

import (
	"log"
	"net/http"
	"os"
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
		cl.DHT().WriteStatus(w)
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
			f, err := os.Create(t.Info().Name + ".torrent")
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
