package main

import (
	"errors"
	"expvar"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"golang.org/x/time/rate"
)

func torrentBar(t *torrent.Torrent, pieceStates bool) {
	go func() {
		start := time.Now()
		if t.Info() == nil {
			fmt.Printf("%v: getting torrent info for %q\n", time.Since(start), t.Name())
			<-t.GotInfo()
		}
		lastStats := t.Stats()
		var lastLine string
		interval := 3 * time.Second
		for range time.Tick(interval) {
			var completedPieces, partialPieces int
			psrs := t.PieceStateRuns()
			for _, r := range psrs {
				if r.Complete {
					completedPieces += r.Length
				}
				if r.Partial {
					partialPieces += r.Length
				}
			}
			stats := t.Stats()
			byteRate := int64(time.Second)
			byteRate *= stats.BytesReadUsefulData.Int64() - lastStats.BytesReadUsefulData.Int64()
			byteRate /= int64(interval)
			line := fmt.Sprintf(
				"%v: downloading %q: %s/%s, %d/%d pieces completed (%d partial): %v/s\n",
				time.Since(start),
				t.Name(),
				humanize.Bytes(uint64(t.BytesCompleted())),
				humanize.Bytes(uint64(t.Length())),
				completedPieces,
				t.NumPieces(),
				partialPieces,
				humanize.Bytes(uint64(byteRate)),
			)
			if line != lastLine {
				lastLine = line
				os.Stdout.WriteString(line)
			}
			if pieceStates {
				fmt.Println(psrs)
			}
			lastStats = stats
		}
	}()
}

type stringAddr string

func (stringAddr) Network() string   { return "" }
func (me stringAddr) String() string { return string(me) }

func resolveTestPeers(addrs []string) (ret []torrent.PeerInfo) {
	for _, ta := range addrs {
		ret = append(ret, torrent.PeerInfo{
			Addr: stringAddr(ta),
		})
	}
	return
}

func addTorrents(client *torrent.Client, flags downloadFlags) error {
	testPeers := resolveTestPeers(flags.TestPeer)
	for _, arg := range flags.Torrent {
		t, err := func() (*torrent.Torrent, error) {
			if strings.HasPrefix(arg, "magnet:") {
				t, err := client.AddMagnet(arg)
				if err != nil {
					return nil, fmt.Errorf("error adding magnet: %w", err)
				}
				return t, nil
			} else if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
				response, err := http.Get(arg)
				if err != nil {
					return nil, fmt.Errorf("Error downloading torrent file: %s", err)
				}

				metaInfo, err := metainfo.Load(response.Body)
				defer response.Body.Close()
				if err != nil {
					return nil, fmt.Errorf("error loading torrent file %q: %s\n", arg, err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					return nil, fmt.Errorf("adding torrent: %w", err)
				}
				return t, nil
			} else if strings.HasPrefix(arg, "infohash:") {
				t, _ := client.AddTorrentInfoHash(metainfo.NewHashFromHex(strings.TrimPrefix(arg, "infohash:")))
				return t, nil
			} else {
				metaInfo, err := metainfo.LoadFromFile(arg)
				if err != nil {
					return nil, fmt.Errorf("error loading torrent file %q: %s\n", arg, err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					return nil, fmt.Errorf("adding torrent: %w", err)
				}
				return t, nil
			}
		}()
		if err != nil {
			return fmt.Errorf("adding torrent for %q: %w", arg, err)
		}
		if flags.Progress {
			torrentBar(t, flags.PieceStates)
		}
		t.AddPeers(testPeers)
		go func() {
			<-t.GotInfo()
			if len(flags.File) == 0 {
				t.DownloadAll()
				if flags.LinearDiscard {
					r := t.NewReader()
					io.Copy(io.Discard, r)
					r.Close()
				}
			} else {
				for _, f := range t.Files() {
					for _, fileArg := range flags.File {
						if f.DisplayPath() == fileArg {
							f.Download()
							if flags.LinearDiscard {
								r := f.NewReader()
								go func() {
									defer r.Close()
									io.Copy(io.Discard, r)
								}()
							}
						}
					}
				}
			}
		}()
	}
	return nil
}

type downloadFlags struct {
	Debug bool
	DownloadCmd
}

type DownloadCmd struct {
	Mmap               bool           `help:"memory-map torrent data"`
	Seed               bool           `help:"seed after download is complete"`
	Addr               string         `help:"network listen addr"`
	MaxUnverifiedBytes tagflag.Bytes  `help:"maximum number bytes to have pending verification"`
	UploadRate         *tagflag.Bytes `help:"max piece bytes to send per second"`
	DownloadRate       *tagflag.Bytes `help:"max bytes per second down from peers"`
	PackedBlocklist    string
	PublicIP           net.IP
	Progress           bool `default:"true"`
	PieceStates        bool `help:"Output piece state runs at progress intervals."`
	Quiet              bool `help:"discard client logging"`
	Stats              bool `help:"print stats at termination"`
	Dht                bool `default:"true"`

	TcpPeers        bool `default:"true"`
	UtpPeers        bool `default:"true"`
	Webtorrent      bool `default:"true"`
	DisableWebseeds bool
	// Don't progress past handshake for peer connections where the peer doesn't offer the fast
	// extension.
	RequireFastExtension bool

	Ipv4 bool `default:"true"`
	Ipv6 bool `default:"true"`
	Pex  bool `default:"true"`

	LinearDiscard bool     `help:"Read and discard selected regions from start to finish. Useful for testing simultaneous Reader and static file prioritization."`
	TestPeer      []string `help:"addresses of some starting peers"`

	File    []string
	Torrent []string `arity:"+" help:"torrent file path or magnet uri" arg:"positional"`
}

func statsEnabled(flags downloadFlags) bool {
	return flags.Stats
}

func exitSignalHandlers(notify *missinggo.SynchronizedEvent) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	for {
		log.Printf("close signal received: %+v", <-c)
		notify.Set()
	}
}

func downloadErr(flags downloadFlags) error {
	clientConfig := torrent.NewDefaultClientConfig()
	clientConfig.DisableWebseeds = flags.DisableWebseeds
	clientConfig.DisableTCP = !flags.TcpPeers
	clientConfig.DisableUTP = !flags.UtpPeers
	clientConfig.DisableIPv4 = !flags.Ipv4
	clientConfig.DisableIPv6 = !flags.Ipv6
	clientConfig.DisableAcceptRateLimiting = true
	clientConfig.NoDHT = !flags.Dht
	clientConfig.Debug = flags.Debug
	clientConfig.Seed = flags.Seed
	clientConfig.PublicIp4 = flags.PublicIP
	clientConfig.PublicIp6 = flags.PublicIP
	clientConfig.DisablePEX = !flags.Pex
	clientConfig.DisableWebtorrent = !flags.Webtorrent
	if flags.PackedBlocklist != "" {
		blocklist, err := iplist.MMapPackedFile(flags.PackedBlocklist)
		if err != nil {
			return fmt.Errorf("loading blocklist: %v", err)
		}
		defer blocklist.Close()
		clientConfig.IPBlocklist = blocklist
	}
	if flags.Mmap {
		clientConfig.DefaultStorage = storage.NewMMap("")
	}
	if flags.Addr != "" {
		clientConfig.SetListenAddr(flags.Addr)
	}
	if flags.UploadRate != nil {
		// TODO: I think the upload rate limit could be much lower.
		clientConfig.UploadRateLimiter = rate.NewLimiter(rate.Limit(*flags.UploadRate), 256<<10)
	}
	if flags.DownloadRate != nil {
		clientConfig.DownloadRateLimiter = rate.NewLimiter(rate.Limit(*flags.DownloadRate), 1<<16)
	}
	if flags.Quiet {
		clientConfig.Logger = log.Discard
	}
	if flags.RequireFastExtension {
		clientConfig.MinPeerExtensions.SetBit(pp.ExtensionBitFast, true)
	}
	clientConfig.MaxUnverifiedBytes = flags.MaxUnverifiedBytes.Int64()

	var stop missinggo.SynchronizedEvent
	defer func() {
		stop.Set()
	}()

	client, err := torrent.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	var clientClose sync.Once // In certain situations, close was being called more than once.
	defer clientClose.Do(func() { client.Close() })
	go exitSignalHandlers(&stop)
	go func() {
		<-stop.C()
		clientClose.Do(func() { client.Close() })
	}()

	// Write status on the root path on the default HTTP muxer. This will be bound to localhost
	// somewhere if GOPPROF is set, thanks to the envpprof import.
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	err = addTorrents(client, flags)
	started := time.Now()
	if err != nil {
		return fmt.Errorf("adding torrents: %w", err)
	}
	defer outputStats(client, flags)
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		err = errors.New("y u no complete torrents?!")
	}
	clientConnStats := client.ConnStats()
	log.Printf("average download rate: %v",
		humanize.Bytes(uint64(
			time.Duration(
				clientConnStats.BytesReadUsefulData.Int64(),
			)*time.Second/time.Since(started),
		)))
	if flags.Seed {
		if len(client.Torrents()) == 0 {
			log.Print("no torrents to seed")
		} else {
			outputStats(client, flags)
			<-stop.C()
		}
	}
	spew.Dump(expvar.Get("torrent").(*expvar.Map).Get("chunks received"))
	spew.Dump(client.ConnStats())
	clStats := client.ConnStats()
	sentOverhead := clStats.BytesWritten.Int64() - clStats.BytesWrittenData.Int64()
	log.Printf(
		"client read %v, %.1f%% was useful data. sent %v non-data bytes",
		humanize.Bytes(uint64(clStats.BytesRead.Int64())),
		100*float64(clStats.BytesReadUsefulData.Int64())/float64(clStats.BytesRead.Int64()),
		humanize.Bytes(uint64(sentOverhead)))
	return err
}

func outputStats(cl *torrent.Client, args downloadFlags) {
	if !statsEnabled(args) {
		return
	}
	expvar.Do(func(kv expvar.KeyValue) {
		fmt.Printf("%s: %s\n", kv.Key, kv.Value)
	})
	cl.WriteStatus(os.Stdout)
}
