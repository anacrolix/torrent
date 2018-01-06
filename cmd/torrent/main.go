// Downloads torrents from the command-line.
package main

import (
	"expvar"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anacrolix/dht"
	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"
	"github.com/dustin/go-humanize"
	"github.com/gosuri/uiprogress"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func torrentBar(t *torrent.Torrent) {
	bar := uiprogress.AddBar(1)
	bar.AppendCompleted()
	bar.AppendFunc(func(*uiprogress.Bar) (ret string) {
		select {
		case <-t.GotInfo():
		default:
			return "getting info"
		}
		if t.Seeding() {
			return "seeding"
		} else if t.BytesCompleted() == t.Info().TotalLength() {
			return "completed"
		} else {
			return fmt.Sprintf("downloading (%s/%s)", humanize.Bytes(uint64(t.BytesCompleted())), humanize.Bytes(uint64(t.Info().TotalLength())))
		}
	})
	bar.PrependFunc(func(*uiprogress.Bar) string {
		return t.Name()
	})
	go func() {
		<-t.GotInfo()
		tl := int(t.Info().TotalLength())
		if tl == 0 {
			bar.Set(1)
			return
		}
		bar.Total = tl
		for {
			bc := t.BytesCompleted()
			bar.Set(int(bc))
			time.Sleep(time.Second)
		}
	}()
}

func addTorrents(client *torrent.Client, download_started chan int) {
	if len(flags.InnerPath) != 0 {
		download_started <- -len(flags.InnerPath)
	} else {
		download_started <- -len(flags.Torrent)
	}
	for _, arg := range flags.Torrent {
		t := func() *torrent.Torrent {
			if strings.HasPrefix(arg, "magnet:") {
				t, err := client.AddMagnet(arg)
				if err != nil {
					log.Fatalf("error adding magnet: %s", err)
				}
				return t
			} else if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
				response, err := http.Get(arg)
				if err != nil {
					log.Fatalf("Error downloading torrent file: %s", err)
				}

				metaInfo, err := metainfo.Load(response.Body)
				defer response.Body.Close()
				if err != nil {
					fmt.Fprintf(os.Stderr, "error loading torrent file %q: %s\n", arg, err)
					os.Exit(1)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					log.Fatal(err)
				}
				return t
			} else if strings.HasPrefix(arg, "infohash:") {
				t, _ := client.AddTorrentInfoHash(metainfo.NewHashFromHex(strings.TrimPrefix(arg, "infohash:")))
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
		torrentBar(t)
		t.AddPeers(func() (ret []torrent.Peer) {
			for _, ta := range flags.TestPeer {
				ret = append(ret, torrent.Peer{
					IP:   ta.IP,
					Port: ta.Port,
				})
			}
			return
		}())
		go func() {
			<-t.GotInfo()
			if len(flags.InnerPath) == 0 {
				t.DownloadAll()
				download_started <- 1
			} else {
				files := t.Files()
				for _, file := range files {
					for _, target := range flags.InnerPath {
						if target == file.DisplayPath() {
							file.Download()
							download_started <- 1
						}
					}
				}
			}
		}()
	}
}

var flags = struct {
	Mmap         bool           `help:"memory-map torrent data"`
	TestPeer     []*net.TCPAddr `help:"addresses of some starting peers"`
	Seed         bool           `help:"seed after download is complete"`
	Addr         *net.TCPAddr   `help:"network listen addr"`
	UploadRate   tagflag.Bytes  `help:"max piece bytes to send per second"`
	DownloadRate tagflag.Bytes  `help:"max bytes per second down from peers"`
	Debug        bool
	InnerPath []string `help:"path to file inside torrent to download"`
	tagflag.StartPos
	Torrent []string `arity:"+" help:"torrent file path or magnet uri"`
}{
	UploadRate:   -1,
	DownloadRate: -1,
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	tagflag.Parse(&flags)
	clientConfig := torrent.Config{
		DHTConfig: dht.ServerConfig{
			StartingNodes: dht.GlobalBootstrapAddrs,
		},
	}
	if flags.Mmap {
		clientConfig.DefaultStorage = storage.NewMMap("")
	}
	if flags.Addr != nil {
		clientConfig.ListenAddr = flags.Addr.String()
	}
	if flags.Seed {
		clientConfig.Seed = true
	}
	if flags.UploadRate != -1 {
		clientConfig.UploadRateLimiter = rate.NewLimiter(rate.Limit(flags.UploadRate), 256<<10)
	}
	if flags.DownloadRate != -1 {
		clientConfig.DownloadRateLimiter = rate.NewLimiter(rate.Limit(flags.DownloadRate), 1<<20)
	}

	client, err := torrent.NewClient(&clientConfig)
	if err != nil {
		log.Fatalf("error creating client: %s", err)
	}
	defer client.Close()
	// Write status on the root path on the default HTTP muxer. This will be
	// bound to localhost somewhere if GOPPROF is set, thanks to the envpprof
	// import.
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	uiprogress.Start()
	download_started := make(chan int, 1)
	addTorrents(client, download_started)
	download_started_cnt := 0
	for {
		download_started_cnt += <- download_started
		if download_started_cnt == 0 {
			break
		}
	}
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		log.Fatal("y u no complete torrents?!")
	}
	if flags.Seed {
		select {}
	}
	expvar.Do(func(kv expvar.KeyValue) {
		fmt.Printf("%s: %s\n", kv.Key, kv.Value)
	})
	envpprof.Stop()
}
