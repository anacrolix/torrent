//go:build !windows

// Mounts a FUSE filesystem backed by torrents and magnet links.
package main

import (
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/anacrolix/envpprof"
	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/fuse"
	fusefs "github.com/anacrolix/fuse/fs"
	"github.com/anacrolix/log"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent"
	torrentfs "github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/util/dirwatch"
)

var logger = log.Default.WithNames("main")

var args = struct {
	MetainfoDir string `help:"torrent files in this location describe the contents of the mounted filesystem"`
	DownloadDir string `help:"location to save torrent data"`
	MountDir    string `help:"location the torrent contents are made available"`

	DisableTrackers bool
	TestPeer        *net.TCPAddr
	ReadaheadBytes  tagflag.Bytes
	ListenAddr      *net.TCPAddr
}{
	MetainfoDir: func() string {
		_user, err := user.Current()
		if err != nil {
			panic(err)
		}
		return filepath.Join(_user.HomeDir, ".config/transmission/torrents")
	}(),
	ReadaheadBytes: 10 << 20,
	ListenAddr:     &net.TCPAddr{},
}

func exitSignalHandlers(fs *torrentfs.TorrentFS) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	for {
		<-c
		fs.Destroy()
		err := fuse.Unmount(args.MountDir)
		if err != nil {
			log.Print(err)
		}
	}
}

func addTestPeer(client *torrent.Client) {
	for _, t := range client.Torrents() {
		t.AddPeers([]torrent.PeerInfo{{
			Addr: args.TestPeer,
		}})
	}
}

func main() {
	defer envpprof.Stop()
	err := mainErr()
	if err != nil {
		logger.Levelf(log.Error, "error in main: %v", err)
		os.Exit(1)
	}
}

func mainErr() error {
	tagflag.Parse(&args)
	if args.MountDir == "" {
		os.Stderr.WriteString("y u no specify mountpoint?\n")
		os.Exit(2)
	}
	conn, err := fuse.Mount(args.MountDir, fuse.ReadOnly())
	if err != nil {
		return fmt.Errorf("mounting: %w", err)
	}
	defer fuse.Unmount(args.MountDir)
	// TODO: Think about the ramifications of exiting not due to a signal.
	defer conn.Close()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = args.DownloadDir
	cfg.DisableTrackers = args.DisableTrackers
	cfg.NoUpload = true // Ensure that downloads are responsive.
	cfg.SetListenAddr(args.ListenAddr.String())
	client, err := torrent.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("creating torrent client: %w", err)
	}
	// This is naturally exported via GOPPROF=http.
	http.DefaultServeMux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	dw, err := dirwatch.New(args.MetainfoDir)
	if err != nil {
		return fmt.Errorf("watching torrent dir: %w", err)
	}
	dw.Logger = dw.Logger.FilterLevel(log.Info)
	go func() {
		for ev := range dw.Events {
			switch ev.Change {
			case dirwatch.Added:
				if ev.TorrentFilePath != "" {
					_, err := client.AddTorrentFromFile(ev.TorrentFilePath)
					if err != nil {
						log.Printf("error adding torrent from file %q to client: %v", ev.TorrentFilePath, err)
					}
				} else if ev.MagnetURI != "" {
					_, err := client.AddMagnet(ev.MagnetURI)
					if err != nil {
						log.Printf("error adding magnet: %s", err)
					}
				}
			case dirwatch.Removed:
				T, ok := client.Torrent(ev.InfoHash)
				if !ok {
					break
				}
				T.Drop()
			}
		}
	}()
	fs := torrentfs.New(client)
	go exitSignalHandlers(fs)

	if args.TestPeer != nil {
		go func() {
			for {
				addTestPeer(client)
				time.Sleep(10 * time.Second)
			}
		}()
	}

	logger.Levelf(log.Debug, "serving fuse fs")
	if err := fusefs.Serve(conn, fs); err != nil {
		return fmt.Errorf("serving fuse fs: %w", err)
	}
	logger.Levelf(log.Debug, "fuse fs completed successfully. waiting for conn ready")
	<-conn.Ready
	if err := conn.MountError; err != nil {
		return fmt.Errorf("mount error: %w", err)
	}
	return nil
}
