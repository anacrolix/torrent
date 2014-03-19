package main

import (
	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/fs"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
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
)

var (
	downloadDir     string
	torrentPath     string
	mountDir        string
	disableTrackers = flag.Bool("disableTrackers", false, "disables trackers")
	testPeer        = flag.String("testPeer", "", "the address for a test peer")
	pprofAddr       = flag.String("pprofAddr", "", "pprof HTTP server bind address")
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
		<-c
		fuse.Unmount(mountDir)
	}()
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
	if *pprofAddr != "" {
		go http.ListenAndServe(*pprofAddr, nil)
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
		DataDir:         downloadDir,
		DisableTrackers: *disableTrackers,
	}
	client.Start()
	torrentDir, err := os.Open(torrentPath)
	defer torrentDir.Close()
	if err != nil {
		log.Fatal(err)
	}
	names, err := torrentDir.Readdirnames(-1)
	if err != nil {
		log.Fatal(err)
	}
	resolveTestPeerAddr()
	for _, name := range names {
		metaInfo, err := metainfo.LoadFromFile(filepath.Join(torrentPath, name))
		if err != nil {
			log.Print(err)
		}
		err = client.AddTorrent(metaInfo)
		if err != nil {
			log.Print(err)
		}
	}
	fs := torrentfs.New(client)
	go func() {
		for {
		torrentLoop:
			for _, t := range client.Torrents() {
				client.Lock()
				for _, c := range t.Conns {
					if c.Socket.RemoteAddr().String() == testPeerAddr.String() {
						client.Unlock()
						continue torrentLoop
					}
				}
				client.Unlock()
				if testPeerAddr != nil {
					if err := client.AddPeers(t.InfoHash, []torrent.Peer{{
						IP:   testPeerAddr.IP,
						Port: testPeerAddr.Port,
					}}); err != nil {
						log.Print(err)
					}
				}
			}
			time.Sleep(10 * time.Second)
		}
	}()
	if err := fusefs.Serve(conn, fs); err != nil {
		log.Fatal(err)
	}
}
