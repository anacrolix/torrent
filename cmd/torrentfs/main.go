// Mounts a FUSE filesystem backed by torrents and magnet links.
package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"github.com/anacrolix/dht"
	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/tagflag"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/fs"
	"github.com/anacrolix/torrent/util/dirwatch"
)

var (
	args = struct {
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
				log.Fatal(err)
			}
			return filepath.Join(_user.HomeDir, ".config/transmission/torrents")
		}(),
		ReadaheadBytes: 10 << 20,
		ListenAddr:     &net.TCPAddr{},
	}
)

func exitSignalHandlers(fs *torrentfs.TorrentFS) {
	c := make(chan os.Signal)
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
		t.AddPeers([]torrent.Peer{{
			IP:   args.TestPeer.IP,
			Port: args.TestPeer.Port,
		}})
	}
}

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	tagflag.Parse(&args)
	if args.MountDir == "" {
		os.Stderr.WriteString("y u no specify mountpoint?\n")
		return 2
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	conn, err := fuse.Mount(args.MountDir)
	if err != nil {
		log.Fatal(err)
	}
	defer fuse.Unmount(args.MountDir)
	// TODO: Think about the ramifications of exiting not due to a signal.
	defer conn.Close()
	client, err := torrent.NewClient(&torrent.Config{
		DataDir:         args.DownloadDir,
		DisableTrackers: args.DisableTrackers,
		ListenAddr:      args.ListenAddr.String(),
		NoUpload:        true, // Ensure that downloads are responsive.
		DHTConfig: dht.ServerConfig{
			StartingNodes: dht.GlobalBootstrapAddrs,
		},
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	// This is naturally exported via GOPPROF=http.
	http.DefaultServeMux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	dw, err := dirwatch.New(args.MetainfoDir)
	if err != nil {
		log.Printf("error watching torrent dir: %s", err)
		return 1
	}
	go func() {
		for ev := range dw.Events {
			switch ev.Change {
			case dirwatch.Added:
				if ev.TorrentFilePath != "" {
					_, err := client.AddTorrentFromFile(ev.TorrentFilePath)
					if err != nil {
						log.Printf("error adding torrent to client: %s", err)
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

	if err := fusefs.Serve(conn, fs); err != nil {
		log.Fatal(err)
	}
	<-conn.Ready
	if err := conn.MountError; err != nil {
		log.Fatal(err)
	}
	return 0
}
