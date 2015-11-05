// Downloads torrents from the command-line.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"
	"github.com/dustin/go-humanize"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/data/mmap"
	"github.com/anacrolix/torrent/metainfo"
)

func resolvedPeerAddrs(ss []string) (ret []torrent.Peer, err error) {
	for _, s := range ss {
		var addr *net.TCPAddr
		addr, err = net.ResolveTCPAddr("tcp", s)
		if err != nil {
			return
		}
		ret = append(ret, torrent.Peer{
			IP:   addr.IP,
			Port: addr.Port,
		})
	}
	return
}

func bytesCompleted(tc *torrent.Client) (ret int64) {
	for _, t := range tc.Torrents() {
		if t.Info() != nil {
			ret += t.BytesCompleted()
		}
	}
	return
}

// Returns an estimate of the total bytes for all torrents.
func totalBytesEstimate(tc *torrent.Client) (ret int64) {
	var noInfo, hadInfo int64
	for _, t := range tc.Torrents() {
		info := t.Info()
		if info == nil {
			noInfo++
			continue
		}
		ret += info.TotalLength()
		hadInfo++
	}
	if hadInfo != 0 {
		// Treat each torrent without info as the average of those with,
		// rounded up.
		ret += (noInfo*ret + hadInfo - 1) / hadInfo
	}
	return
}

func progressLine(tc *torrent.Client) string {
	return fmt.Sprintf("\033[K%s / %s\r", humanize.Bytes(uint64(bytesCompleted(tc))), humanize.Bytes(uint64(totalBytesEstimate(tc))))
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	var opts struct {
		torrent.Config `name:"Client"`
		Mmap           bool           `help:"memory-map torrent data"`
		TestPeer       []*net.TCPAddr `short:"p" help:"addresses of some starting peers"`
		Torrent        []string       `type:"pos" arity:"+" help:"torrent file path or magnet uri"`
	}
	tagflag.Parse(&opts, tagflag.SkipBadTypes())
	clientConfig := opts.Config
	if opts.Mmap {
		clientConfig.TorrentDataOpener = func(info *metainfo.Info) torrent.Data {
			ret, err := mmap.TorrentData(info, "")
			if err != nil {
				log.Fatalf("error opening torrent data for %q: %s", info.Name, err)
			}
			return ret
		}
	}

	torrents := opts.Torrent
	if len(torrents) == 0 {
		fmt.Fprintf(os.Stderr, "no torrents specified\n")
		return
	}
	client, err := torrent.NewClient(&clientConfig)
	if err != nil {
		log.Fatalf("error creating client: %s", err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	defer client.Close()
	for _, arg := range torrents {
		t := func() torrent.Torrent {
			if strings.HasPrefix(arg, "magnet:") {
				t, err := client.AddMagnet(arg)
				if err != nil {
					log.Fatalf("error adding magnet: %s", err)
				}
				return t
			} else {
				metaInfo, err := metainfo.LoadFromFile(arg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "error loading torrent file %q: %s\n", arg, err)
					os.Exit(1)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					log.Fatal(err)
				}
				return t
			}
		}()
		err := t.AddPeers(func() (ret []torrent.Peer) {
			for _, ta := range opts.TestPeer {
				ret = append(ret, torrent.Peer{
					IP:   ta.IP,
					Port: ta.Port,
				})
			}
			return
		}())
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			<-t.GotInfo()
			t.DownloadAll()
		}()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if client.WaitAll() {
			log.Print("downloaded ALL the torrents")
		} else {
			log.Fatal("y u no complete torrents?!")
		}
	}()
	ticker := time.NewTicker(time.Second)
waitDone:
	for {
		select {
		case <-done:
			break waitDone
		case <-ticker.C:
			os.Stdout.WriteString(progressLine(client))
		}
	}
	if opts.Seed {
		select {}
	}
}
