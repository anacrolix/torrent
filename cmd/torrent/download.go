package main

import (
	"context"
	"expvar"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/tagflag"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

func clientStatusWriter(ctx context.Context, cl *torrent.Client) {
	start := time.Now()
	lastStats := cl.Stats()
	var lastLine string
	var lastPrint time.Time
	interval := 3 * time.Second
	ticker := time.Tick(interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker:
		}
		stats := cl.Stats()
		ts := cl.Torrents()
		var completeBytes int64
		var totalBytes int64
		var infos int
		for _, t := range ts {
			if t.Info() != nil {
				infos++
				completeBytes += t.BytesCompleted()
				totalBytes += t.Info().TotalLength()
			}
		}
		getRate := func(a func(*torrent.ClientStats) *torrent.Count) int64 {
			byteRate := int64(time.Second)
			byteRate *= a(&stats).Int64() - a(&lastStats).Int64()
			byteRate /= int64(interval)
			return byteRate
		}
		uploadRate := getRate(func(s *torrent.ClientStats) *torrent.Count {
			return &s.BytesWrittenData
		})
		downloadRate := getRate(func(s *torrent.ClientStats) *torrent.Count {
			return &s.BytesReadUsefulData
		})
		line := fmt.Sprintf(
			"%v torrents, %v infos, %s/%s ready, upload %s, download %s/s",
			len(ts),
			infos,
			humanize.Bytes(uint64(completeBytes)),
			humanize.Bytes(uint64(totalBytes)),
			humanize.Bytes(uint64(uploadRate)),
			humanize.Bytes(uint64(downloadRate)),
		)
		if line != lastLine || time.Since(lastPrint) >= time.Minute {
			lastLine = line
			lastPrint = time.Now()
			fmt.Fprintf(os.Stdout, "%s: %s\n", time.Since(start), line)
		}
		lastStats = stats
	}
}

// Keeping this for now for reference in case I do per-torrent deltas in client status updates.
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

func addTorrents(
	ctx context.Context,
	client *torrent.Client,
	flags downloadFlags,
	wg *sync.WaitGroup,
	fatalErr func(err error),
) error {
	testPeers := resolveTestPeers(flags.TestPeer)
	for _, arg := range flags.Torrent {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
					return nil, fmt.Errorf("error downloading torrent file: %w", err)
				}

				metaInfo, err := metainfo.Load(response.Body)
				defer response.Body.Close()
				if err != nil {
					return nil, fmt.Errorf("error loading torrent file %q: %s\n", arg, err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					return nil, err
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
		t.SetOnWriteChunkError(func(err error) {
			err = fmt.Errorf("error writing chunk for %v: %w", t, err)
			fatalErr(err)
		})
		t.AddPeers(testPeers)
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case <-t.GotInfo():
			}
			if flags.SaveMetainfos {
				path := fmt.Sprintf("%s.torrent", t.InfoHash().HexString())
				err := writeMetainfoToFile(t.Metainfo(), path)
				if err == nil {
					log.Printf("wrote %q", path)
				} else {
					log.Printf("error writing %q: %v", path, err)
				}
			}
			if flags.Verify {
				err := t.VerifyDataContext(ctx)
				if err != nil && ctx.Err() == nil {
					log.Levelf(log.Error, "error verifying data: %v", err)
				}
			}
			if len(flags.File) == 0 {
				t.DownloadAll()
				wg.Add(1)
				go func() {
					defer wg.Done()
					waitForPieces(ctx, t, 0, t.NumPieces())
				}()
				done := make(chan struct{})
				go func() {
					defer close(done)
					if flags.LinearDiscard {
						r := t.NewReader()
						io.Copy(io.Discard, r)
						r.Close()
					}
				}()
				select {
				case <-done:
				case <-ctx.Done():
				}
			} else {
				for _, f := range t.Files() {
					for _, fileArg := range flags.File {
						if f.DisplayPath() == fileArg {
							wg.Add(1)
							go func() {
								defer wg.Done()
								waitForPieces(ctx, t, f.BeginPieceIndex(), f.EndPieceIndex())
							}()
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

func waitForPieces(ctx context.Context, t *torrent.Torrent, beginIndex, endIndex int) {
	sub := t.SubscribePieceStateChanges()
	defer sub.Close()
	expected := storage.Completion{
		Complete: true,
		Ok:       true,
	}
	pending := make(map[int]struct{})
	for i := beginIndex; i < endIndex; i++ {
		if t.Piece(i).State().Completion != expected {
			pending[i] = struct{}{}
		}
	}
	for {
		if len(pending) == 0 {
			return
		}
		select {
		case ev := <-sub.Values:
			if ev.Completion == expected {
				delete(pending, ev.Index)
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeMetainfoToFile(mi metainfo.MetaInfo, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	err = mi.Write(f)
	if err != nil {
		return err
	}
	return f.Close()
}

type downloadFlags struct {
	Debug bool
	DownloadCmd
}

type DownloadCmd struct {
	SaveMetainfos                  bool           `help:"save metainfo files when info is obtained"`
	Mmap                           bool           `help:"memory-map torrent data"`
	Seed                           bool           `help:"seed after download is complete"`
	Addr                           string         `help:"network listen addr"`
	MaxUnverifiedBytes             *tagflag.Bytes `help:"maximum number bytes to have pending verification"`
	UploadRate                     *tagflag.Bytes `help:"max piece bytes to send per second"`
	MaxAllocPeerRequestDataPerConn *tagflag.Bytes `help:"max bytes to allocate for peer request data per connection"`
	DownloadRate                   *tagflag.Bytes `help:"max bytes per second down from peers"`
	PackedBlocklist                string
	PublicIP                       net.IP
	Progress                       bool `default:"true"`
	PieceStates                    bool `help:"Output piece state runs at progress intervals."`
	Quiet                          bool `help:"discard client logging"`
	Stats                          bool `help:"print stats at termination"`
	Dht                            bool `default:"true"`
	PortForward                    bool `default:"true"`
	Verify                         bool `help:"verify data after adding torrent"`

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

func downloadErr(ctx context.Context, flags downloadFlags) error {
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
	clientConfig.PublicIp4 = flags.PublicIP.To4()
	clientConfig.PublicIp6 = flags.PublicIP
	clientConfig.DisablePEX = !flags.Pex
	clientConfig.DisableWebtorrent = !flags.Webtorrent
	clientConfig.NoDefaultPortForwarding = !flags.PortForward
	if flags.MaxAllocPeerRequestDataPerConn != nil {
		clientConfig.MaxAllocPeerRequestDataPerConn = int(flags.MaxAllocPeerRequestDataPerConn.Int64())
	}
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
		clientConfig.UploadRateLimiter = rate.NewLimiter(
			rate.Limit(*flags.UploadRate),
			// Need to ensure the expected peer request length <= the upload
			// burst. We can't really encode this logic into the ClientConfig as
			// helper because it's quite specific. We're assuming the
			// MaxAllocPeerRequestDataPerConn flag is being used to support
			// this.
			max(int(*flags.MaxAllocPeerRequestDataPerConn), 256<<10))
	}
	if flags.DownloadRate != nil {
		clientConfig.DownloadRateLimiter = rate.NewLimiter(rate.Limit(*flags.DownloadRate), 1<<16)
	}
	{
		logger := log.Default.WithNames("main", "client")
		if flags.Quiet {
			logger = logger.FilterLevel(log.Critical)
		}
		clientConfig.Logger = logger
	}
	if flags.RequireFastExtension {
		clientConfig.MinPeerExtensions.SetBit(pp.ExtensionBitFast, true)
	}
	if flags.MaxUnverifiedBytes != nil {
		clientConfig.MaxUnverifiedBytes = flags.MaxUnverifiedBytes.Int64()
	}

	client, err := torrent.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	defer client.Close()

	if flags.Progress {
		go clientStatusWriter(ctx, client)
	}

	// Write status on the root path on the default HTTP muxer. This will be bound to localhost
	// somewhere if GOPPROF is set, thanks to the envpprof import.
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	var wg sync.WaitGroup
	fatalErr := make(chan error, 1)
	err = addTorrents(ctx, client, flags, &wg,
		func(err error) {
			select {
			case fatalErr <- err:
			default:
				panic(err)
			}
		})
	if err != nil {
		return fmt.Errorf("adding torrents: %w", err)
	}
	started := time.Now()
	defer outputStats(client, flags)
	wgWaited := make(chan struct{})
	go func() {
		defer close(wgWaited)
		wg.Wait()
	}()
	select {
	case <-wgWaited:
		if ctx.Err() == nil {
			log.Print("downloaded ALL the torrents")
		} else {
			err = ctx.Err()
		}
	case err = <-fatalErr:
	}
	clientConnStats := client.ConnStats()
	log.Printf(
		"average download rate: %v/s",
		humanize.Bytes(uint64(float64(
			clientConnStats.BytesReadUsefulData.Int64(),
		)/time.Since(started).Seconds())),
	)
	if flags.Seed {
		if len(client.Torrents()) == 0 {
			log.Print("no torrents to seed")
		} else {
			outputStats(client, flags)
			<-ctx.Done()
		}
	}
	fmt.Printf("chunks received: %v\n", &torrent.ChunksReceived)
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
