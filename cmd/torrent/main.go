// Downloads torrents from the command-line.
// 
// Example run:
// $ go run cmd/torrent/main.go download --addr localhost:42070 https://releases.ubuntu.com/20.04/ubuntu-20.04.2-live-server-amd64.iso.torrent    
// 2021-08-10T02:02:24-0700 NONE  client.go:395: dht server on 127.0.0.1:42070 (node id 34169972f3528b159c0b02be758a084793fcea9a) completed bootstrap (dht.TraversalStats{NumAddrsTried:9, NumResponses:0})
// 2021-08-10T02:02:24-0700 NONE  client.go:395: dht server on [::1]:42070 (node id 5add5bc7b138f7b149fa70cd00a24312995f938f) completed bootstrap (dht.TraversalStats{NumAddrsTried:9, NumResponses:0})
// 1.03236333s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 0 B/1.2 GB, 0/4636 pieces completed (0 partial): 0 B/s
// 2.001421913s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 475 kB/1.2 GB, 1/4636 pieces completed (2 partial): 475 kB/s
// 3.143916633s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 3.5 MB/1.2 GB, 10/4636 pieces completed (9 partial): 3.0 MB/s
// 4.045550543s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 8.9 MB/1.2 GB, 21/4636 pieces completed (19 partial): 5.4 MB/s
// 5.001778786s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 18 MB/1.2 GB, 65/4636 pieces completed (10 partial): 8.7 MB/s
// 6.092551802s: downloading "ubuntu-20.04.2-live-server-amd64.iso": 23 MB/1.2 GB, 86/4636 pieces completed (9 partial): 5.8 MB/s
// ...

package main

import (
	"expvar"
	"fmt"
	"io"
	stdLog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/anacrolix/envpprof"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/version"
	"github.com/davecgh/go-spew/spew"
	"github.com/dustin/go-humanize"
	"golang.org/x/xerrors"

	"github.com/anacrolix/log"

	"github.com/anacrolix/tagflag"
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
	for _, ta := range flags.TestPeer {
		ret = append(ret, torrent.PeerInfo{
			Addr: stringAddr(ta),
		})
	}
	return
}

func addTorrents(client *torrent.Client) error {
	testPeers := resolveTestPeers(flags.TestPeer)
	for _, arg := range flags.Torrent {
		t, err := func() (*torrent.Torrent, error) {
			if strings.HasPrefix(arg, "magnet:") {
				t, err := client.AddMagnet(arg)
				if err != nil {
					return nil, xerrors.Errorf("error adding magnet: %w", err)
				}
				return t, nil
			} else if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
				response, err := http.Get(arg)
				if err != nil {
					return nil, xerrors.Errorf("Error downloading torrent file: %s", err)
				}

				metaInfo, err := metainfo.Load(response.Body)
				defer response.Body.Close()
				if err != nil {
					return nil, xerrors.Errorf("error loading torrent file %q: %s\n", arg, err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					return nil, xerrors.Errorf("adding torrent: %w", err)
				}
				return t, nil
			} else if strings.HasPrefix(arg, "infohash:") {
				t, _ := client.AddTorrentInfoHash(metainfo.NewHashFromHex(strings.TrimPrefix(arg, "infohash:")))
				return t, nil
			} else {
				metaInfo, err := metainfo.LoadFromFile(arg)
				if err != nil {
					return nil, xerrors.Errorf("error loading torrent file %q: %s\n", arg, err)
				}
				t, err := client.AddTorrent(metaInfo)
				if err != nil {
					return nil, xerrors.Errorf("adding torrent: %w", err)
				}
				return t, nil
			}
		}()
		if err != nil {
			return xerrors.Errorf("adding torrent for %q: %w", arg, err)
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

var flags struct {
	Debug bool

	*DownloadCmd      `arg:"subcommand:download"`
	*ListFilesCmd     `arg:"subcommand:list-files"`
	*SpewBencodingCmd `arg:"subcommand:spew-bencoding"`
	//*AnnounceCmd      `arg:"subcommand:announce"`
	*VersionCmd       `arg:"subcommand:version"`
}

type VersionCmd struct{}

type SpewBencodingCmd struct{}

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

type ListFilesCmd struct {
	TorrentPath string `arg:"positional"`
}

func stdoutAndStderrAreSameFile() bool {
	fi1, _ := os.Stdout.Stat()
	fi2, _ := os.Stderr.Stat()
	return os.SameFile(fi1, fi2)
}

func statsEnabled() bool {
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
	p := arg.MustParse(&flags)
	switch {
	//case flags.AnnounceCmd != nil:
	//	return announceErr()
	//case :
	//	return announceErr(flags.Args, parser)
	case flags.DownloadCmd != nil:
		return downloadErr()
	case flags.ListFilesCmd != nil:
		mi, err := metainfo.LoadFromFile(flags.ListFilesCmd.TorrentPath)
		if err != nil {
			return fmt.Errorf("loading from file %q: %v", flags.ListFilesCmd.TorrentPath, err)
		}
		info, err := mi.UnmarshalInfo()
		if err != nil {
			return fmt.Errorf("unmarshalling info from metainfo at %q: %v", flags.ListFilesCmd.TorrentPath, err)
		}
		for _, f := range info.UpvertedFiles() {
			fmt.Println(f.DisplayPath(&info))
		}
		return nil
	case flags.SpewBencodingCmd != nil:
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
	case flags.VersionCmd != nil:
		fmt.Printf("HTTP User-Agent: %q\n", version.DefaultHttpUserAgent)
		fmt.Printf("Torrent client version: %q\n", version.DefaultExtendedHandshakeClientVersion)
		fmt.Printf("Torrent version prefix: %q\n", version.DefaultBep20Prefix)
		return nil
	default:
		p.Fail(fmt.Sprintf("unexpected subcommand: %v", p.Subcommand()))
		panic("unreachable")
	}
}

func downloadErr() error {
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
			return xerrors.Errorf("loading blocklist: %v", err)
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
		return xerrors.Errorf("creating client: %v", err)
	}
	defer client.Close()
	go exitSignalHandlers(&stop)
	go func() {
		<-stop.C()
		client.Close()
	}()

	// Write status on the root path on the default HTTP muxer. This will be bound to localhost
	// somewhere if GOPPROF is set, thanks to the envpprof import.
	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	err = addTorrents(client)
	if err != nil {
		return fmt.Errorf("adding torrents: %w", err)
	}
	defer outputStats(client)
	if client.WaitAll() {
		log.Print("downloaded ALL the torrents")
	} else {
		return xerrors.New("y u no complete torrents?!")
	}
	if flags.Seed {
		outputStats(client)
		<-stop.C()
	}
	return nil
}

func outputStats(cl *torrent.Client) {
	if !statsEnabled() {
		return
	}
	expvar.Do(func(kv expvar.KeyValue) {
		fmt.Printf("%s: %s\n", kv.Key, kv.Value)
	})
	cl.WriteStatus(os.Stdout)
}
