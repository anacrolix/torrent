package main

import (
	"flag"
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
	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/fs"
	"github.com/anacrolix/libtorgo/metainfo"
)

var (
	downloadDir     string
	torrentPath     string
	mountDir        string
	disableTrackers = flag.Bool("disableTrackers", false, "disables trackers")
	testPeer        = flag.String("testPeer", "", "the address for a test peer")
	httpAddr        = flag.String("httpAddr", "", "HTTP server bind address")
	readaheadBytes  = flag.Int("readaheadBytes", 10*1024*1024, "bytes to readahead in each torrent from the last read piece")
	testPeerAddr    *net.TCPAddr
)

func init() {
	flag.StringVar(&downloadDir, "downloadDir", "", "location to save torrent data")
	flag.StringVar(&torrentPath, "torrentPath", func() string {
		_user, err := user.Current()
		if err != nil {
			log.Fatal(err)
		}
		return filepath.Join(_user.HomeDir, ".config/transmission/torrents")
	}(), "torrent files in this location describe the contents of the mounted filesystem")
	flag.StringVar(&mountDir, "mountDir", "", "location the torrent contents are made available")
}

func resolveTestPeerAddr() {
	if *testPeer == "" {
		return
	}
	var err error
	testPeerAddr, err = net.ResolveTCPAddr("tcp4", *testPeer)
	if err != nil {
		log.Fatal(err)
	}
}

func setSignalHandlers() {
	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for {
			<-c
			err := fuse.Unmount(mountDir)
			if err != nil {
				log.Print(err)
			}
		}
	}()
}

func addTorrent(c *torrent.Client, file string) {
	metaInfo, err := metainfo.LoadFromFile(file)
	if err != nil {
		log.Print(err)
		return
	}
	err = c.AddTorrent(metaInfo)
	if err != nil {
		log.Print(err)
		return
	}
}

func addTorrentDir(c *torrent.Client, _path string) {
	torrentDir, err := os.Open(torrentPath)
	defer torrentDir.Close()
	if err != nil {
		log.Fatal(err)
	}
	names, err := torrentDir.Readdirnames(-1)
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range names {
		go addTorrent(c, filepath.Join(_path, name))
	}
}

func addTestPeer(client *torrent.Client) {
	for _, t := range client.Torrents() {
		if testPeerAddr != nil {
			if err := client.AddPeers(t.InfoHash, []torrent.Peer{{
				IP:   testPeerAddr.IP,
				Port: testPeerAddr.Port,
			}}); err != nil {
				log.Print(err)
			}
		}
	}
}

func main() {
	flag.Parse()
	if flag.NArg() != 0 {
		os.Stderr.WriteString("one does not simply pass positional args\n")
		os.Exit(2)
	}
	if mountDir == "" {
		os.Stderr.WriteString("y u no specify mountpoint?\n")
		os.Exit(2)
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if *httpAddr != "" {
		go http.ListenAndServe(*httpAddr, nil)
	}
	conn, err := fuse.Mount(mountDir)
	if err != nil {
		log.Fatal(err)
	}
	defer fuse.Unmount(mountDir)
	// TODO: Think about the ramifications of exiting not due to a signal.
	setSignalHandlers()
	defer conn.Close()
	client := &torrent.Client{
		DataDir:          downloadDir,
		DisableTrackers:  *disableTrackers,
		DownloadStrategy: &torrent.ResponsiveDownloadStrategy{*readaheadBytes},
	}
	http.DefaultServeMux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		client.WriteStatus(w)
	})
	client.Start()
	addTorrentDir(client, torrentPath)
	resolveTestPeerAddr()
	fs := torrentfs.New(client)
	go func() {
		for {
			addTestPeer(client)
			time.Sleep(10 * time.Second)
		}
	}()

	if err := fusefs.Serve(conn, fs); err != nil {
		log.Fatal(err)
	}
	<-conn.Ready
	if err := conn.MountError; err != nil {
		log.Fatal(err)
	}
}
