// Downloads torrents from the command-line.
package main

import (
	"errors"
	"expvar"
	"fmt"
	"io"
	stdLog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/args"
	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/tagflag"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/version"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
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
		for range time.Tick(time.Second) {
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
			line := fmt.Sprintf(
				"%v: downloading %q: %s/%s, %d/%d pieces completed (%d partial): %v/s\n",
				time.Since(start),
				t.Name(),
				humanize.Bytes(uint64(t.BytesCompleted())),
				humanize.Bytes(uint64(t.Length())),
				completedPieces,
				t.NumPieces(),
				partialPieces,
				humanize.Bytes(uint64(stats.BytesReadUsefulData.Int64()-lastStats.BytesReadUsefulData.Int64())),
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
			} else {
				for _, f := range t.Files() {
					for _, fileArg := range flags.File {
						if f.DisplayPath() == fileArg {
							f.Download()
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
	TestPeer           []string       `help:"addresses of some starting peers"`
	Seed               bool           `help:"seed after download is complete"`
	Addr               string         `help:"network listen addr"`
	MaxUnverifiedBytes tagflag.Bytes  `help:"maximum number bytes to have pending verification"`
	UploadRate         *tagflag.Bytes `help:"max piece bytes to send per second"`
	DownloadRate       *tagflag.Bytes `help:"max bytes per second down from peers"`
	PackedBlocklist    string
	PublicIP           net.IP
	Progress           bool `default:"true"`
	PieceStates        bool
	Quiet              bool  `help:"discard client logging"`
	Stats              *bool `help:"print stats at termination"`
	Dht                bool  `default:"true"`

	TcpPeers        bool `default:"true"`
	UtpPeers        bool `default:"true"`
	Webtorrent      bool `default:"true"`
	DisableWebseeds bool

	Ipv4 bool `default:"true"`
	Ipv6 bool `default:"true"`
	Pex  bool `default:"true"`

	File    []string
	Torrent []string `arity:"+" help:"torrent file path or magnet uri" arg:"positional"`
}

func stdoutAndStderrAreSameFile() bool {
	fi1, _ := os.Stdout.Stat()
	fi2, _ := os.Stderr.Stat()
	return os.SameFile(fi1, fi2)
}

func statsEnabled(flags downloadFlags) bool {
	if flags.Stats == nil {
		return flags.Debug
	}
	return *flags.Stats
}

func exitSignalHandlers(notify *missinggo.SynchronizedEvent) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	for {
		log.Printf("close signal received: %+v", <-c)
		notify.Set()
	}
}

func main() {
	defer envpprof.Stop()
	if err := mainErr(); err != nil {
		log.Printf("error in main: %v", err)
		os.Exit(1)
	}
}

func mainErr() error {
	stdLog.SetFlags(stdLog.Flags() | stdLog.Lshortfile)
	debug := args.Flag(args.FlagOpt{Long: "debug"})
	p := args.ParseMain(
		debug,
		args.Subcommand("metainfo", metainfoCmd),
		args.Subcommand("announce", func(p args.SubCmdCtx) error {
			var cmd AnnounceCmd
			err := p.NewParser().AddParams(
				args.Pos("tracker", &cmd.Tracker),
				args.Pos("infohash", &cmd.InfoHash)).Parse()
			if err != nil {
				return err
			}
			return announceErr(cmd)
		}),
		args.Subcommand("download", func(p args.SubCmdCtx) error {
			var dlf DownloadCmd
			err := p.NewParser().AddParams(
				append(args.FromStruct(&dlf), debug)...,
			).Parse()
			if err != nil {
				return err
			}
			return downloadErr(downloadFlags{
				Debug:       debug.Bool(),
				DownloadCmd: dlf,
			})
		}),
		args.Subcommand(
			"spew-bencoding",
			func(p args.SubCmdCtx) error {
				d := bencode.NewDecoder(os.Stdin)
				for i := 0; ; i++ {
					var v interface{}
					err := d.Decode(&v)
					if err == io.EOF {
						break
					}
					if err != nil {
						return fmt.Errorf("decoding message index %d: %w", i, err)
					}
					spew.Dump(v)
				}
				return nil
			},
			args.Help("reads bencoding from stdin into Go native types and spews the result"),
		),
		args.Subcommand("version", func(p args.SubCmdCtx) error {
			fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
			fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
			fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
			return nil
		}),
	)
	if p.Err != nil {
		if errors.Is(p.Err, args.ErrHelped) {
			return nil
		}
		return p.Err
	}
	if !p.RanSubCmd {
		p.PrintChoices(os.Stderr)
		args.FatalUsage()
	}
	return nil
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
		clientConfig.UploadRateLimiter = rate.NewLimiter(rate.Limit(*flags.UploadRate), 256<<10)
	}
	if flags.DownloadRate != nil {
		clientConfig.DownloadRateLimiter = rate.NewLimiter(rate.Limit(*flags.DownloadRate), 1<<20)
	}
	if flags.Quiet {
		clientConfig.Logger = log.Discard
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
	var clientClose sync.Once //In certain situations, close was being called more than once.
	defer clientClose.Do(client.Close)
	go exitSignalHandlers(&stop)
	go func() {
		<-stop.C()
		clientClose.Do(client.Close)
	}()

	// Write status on the root path on the default HTTP muxer. This will be bound to localhost
	// somewhere if GOPPROF is set, thanks to the envpprof import.
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	err = addTorrents(client, flags)
	if err != nil {
		return fmt.Errorf("adding torrents: %w", err)
	}
	defer outputStats(client, flags)
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		return errors.New("y u no complete torrents?!")
	}
	if flags.Seed {
		if len(client.Torrents()) == 0 {
			log.Print("no torrents to seed")
		} else {
			outputStats(client, flags)
			<-stop.C()
		}
	}
	return nil
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
