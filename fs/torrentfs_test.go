package torrentfs

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"bitbucket.org/anacrolix/go.torrent"
	"bitbucket.org/anacrolix/go.torrent/testutil"
	"bitbucket.org/anacrolix/go.torrent/util"
	"github.com/anacrolix/libtorgo/metainfo"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
)

func init() {
	go http.ListenAndServe(":6061", nil)
}

func TestTCPAddrString(t *testing.T) {
	ta := &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 3000,
	}
	s := ta.String()
	l, err := net.Listen("tcp4", "localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ras := c.RemoteAddr().String()
	if ras != s {
		t.FailNow()
	}
}

type testLayout struct {
	BaseDir   string
	MountDir  string
	Completed string
	Metainfo  *metainfo.MetaInfo
}

func (me *testLayout) Destroy() error {
	return os.RemoveAll(me.BaseDir)
}

func newGreetingLayout() (tl testLayout, err error) {
	tl.BaseDir, err = ioutil.TempDir("", "torrentfs")
	if err != nil {
		return
	}
	tl.Completed = filepath.Join(tl.BaseDir, "completed")
	os.Mkdir(tl.Completed, 0777)
	tl.MountDir = filepath.Join(tl.BaseDir, "mnt")
	os.Mkdir(tl.MountDir, 0777)
	name := testutil.CreateDummyTorrentData(tl.Completed)
	metaInfoBuf := &bytes.Buffer{}
	testutil.CreateMetaInfo(name, metaInfoBuf)
	tl.Metainfo, err = metainfo.Load(metaInfoBuf)
	log.Printf("%x", tl.Metainfo.Info.Pieces)
	return
}

func TestUnmountWedged(t *testing.T) {
	layout, err := newGreetingLayout()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := layout.Destroy()
		if err != nil {
			t.Log(err)
		}
	}()
	client, err := torrent.NewClient(&torrent.Config{
		DataDir:         filepath.Join(layout.BaseDir, "incomplete"),
		DisableTrackers: true,
		NoDHT:           true,
	})
	log.Printf("%+v", *layout.Metainfo)
	client.AddTorrent(layout.Metainfo)
	fs := New(client)
	fuseConn, err := fuse.Mount(layout.MountDir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		server := fusefs.Server{
			FS: fs,
			Debug: func(msg interface{}) {
				log.Print(msg)
			},
		}
		server.Serve(fuseConn)
	}()
	<-fuseConn.Ready
	if err := fuseConn.MountError; err != nil {
		log.Fatal(err)
	}
	go func() {
		ioutil.ReadFile(filepath.Join(layout.MountDir, layout.Metainfo.Info.Name))
	}()
	time.Sleep(time.Second)
	fs.Destroy()
	time.Sleep(time.Second)
	err = fuse.Unmount(layout.MountDir)
	if err != nil {
		log.Print(err)
	}
	err = fuseConn.Close()
	if err != nil {
		t.Log(err)
	}
}

func TestDownloadOnDemand(t *testing.T) {
	layout, err := newGreetingLayout()
	if err != nil {
		t.Fatal(err)
	}
	seeder, err := torrent.NewClient(&torrent.Config{
		DataDir:         layout.Completed,
		DisableTrackers: true,
		NoDHT:           true,
	})
	http.HandleFunc("/seeder", func(w http.ResponseWriter, req *http.Request) {
		seeder.WriteStatus(w)
	})
	defer seeder.Stop()
	_, err = seeder.AddMagnet(fmt.Sprintf("magnet:?xt=urn:btih:%x", layout.Metainfo.Info.Hash))
	if err != nil {
		t.Fatal(err)
	}
	leecher, err := torrent.NewClient(&torrent.Config{
		DataDir:          filepath.Join(layout.BaseDir, "download"),
		DownloadStrategy: torrent.NewResponsiveDownloadStrategy(0),
		DisableTrackers:  true,
		NoDHT:            true,

		// This can be used to check if clients can connect to other clients
		// with the same ID.

		// PeerID: seeder.PeerID(),
	})
	http.HandleFunc("/leecher", func(w http.ResponseWriter, req *http.Request) {
		leecher.WriteStatus(w)
	})
	defer leecher.Stop()
	leecher.AddTorrent(layout.Metainfo)
	var ih torrent.InfoHash
	util.CopyExact(ih[:], layout.Metainfo.Info.Hash)
	leecher.AddPeers(ih, []torrent.Peer{func() torrent.Peer {
		_, port, err := net.SplitHostPort(seeder.ListenAddr().String())
		if err != nil {
			panic(err)
		}
		portInt64, err := strconv.ParseInt(port, 0, 0)
		if err != nil {
			panic(err)
		}
		return torrent.Peer{
			IP:   net.IPv6loopback,
			Port: int(portInt64),
		}
	}()})
	fs := New(leecher)
	mountDir := layout.MountDir
	fuseConn, err := fuse.Mount(layout.MountDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fuse.Unmount(mountDir); err != nil {
			t.Fatal(err)
		}
	}()
	go func() {
		if err := fusefs.Serve(fuseConn, fs); err != nil {
			t.Fatal(err)
		}
		if err := fuseConn.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	<-fuseConn.Ready
	if fuseConn.MountError != nil {
		t.Fatal(fuseConn.MountError)
	}
	go func() {
		time.Sleep(10 * time.Second)
		if err := fuse.Unmount(mountDir); err != nil {
			t.Log(err)
		}
	}()
	content, err := ioutil.ReadFile(filepath.Join(mountDir, "greeting"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != testutil.GreetingFileContents {
		t.FailNow()
	}
}
