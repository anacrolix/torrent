package main

import (
	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"time"
)

var (
	downloadDir string
	torrentPath string
	mountDir    string
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

func main() {
	pprofAddr := flag.String("pprofAddr", "", "pprof HTTP server bind address")
	testPeer := flag.String("testPeer", "", "the address for a test peer")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if *pprofAddr != "" {
		go http.ListenAndServe(*pprofAddr, nil)
	}
	client := &torrent.Client{
		DataDir:       downloadDir,
		HalfOpenLimit: 2,
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
	var testAddr *net.TCPAddr
	if *testPeer != "" {
		testAddr, err = net.ResolveTCPAddr("tcp4", *testPeer)
		if err != nil {
			log.Fatal(err)
		}
	}
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
	conn, err := fuse.Mount(mountDir)
	if err != nil {
		log.Fatal(err)
	}
	fs := &TorrentFS{
		Client:   client,
		DataSubs: make(map[chan torrent.DataSpec]struct{}),
	}
	go fs.publishData()
	go func() {
		for {
		torrentLoop:
			for _, t := range client.Torrents() {
				client.Lock()
				for _, c := range t.Conns {
					if c.Socket.RemoteAddr().String() == testAddr.String() {
						client.Unlock()
						continue torrentLoop
					}
				}
				client.Unlock()
				if testAddr != nil {
					if err := client.AddPeers(t.InfoHash, []torrent.Peer{{
						IP:   testAddr.IP,
						Port: testAddr.Port,
					}}); err != nil {
						log.Print(err)
					}
				}
			}
			time.Sleep(10 * time.Second)
		}
	}()
	fusefs.Serve(conn, fs)
}
