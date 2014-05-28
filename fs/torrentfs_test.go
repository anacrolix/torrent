package torrentfs

import (
	"bytes"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bitbucket.org/anacrolix/go.torrent/testutil"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	metainfo "github.com/nsf/libtorgo/torrent"
)

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
	client := torrent.Client{
		DataDir:         filepath.Join(layout.BaseDir, "incomplete"),
		DisableTrackers: true,
	}
	client.Start()
	client.AddTorrent(layout.Metainfo)
	fs := New(&client)
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
		ioutil.ReadFile(filepath.Join(layout.MountDir, layout.Metainfo.Name))
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
	seeder := torrent.Client{
		DataDir: layout.Completed,
		Listener: func() net.Listener {
			conn, err := net.Listen("tcp", ":0")
			if err != nil {
				panic(err)
			}
			return conn
		}(),
	}
	defer seeder.Listener.Close()
	seeder.Start()
	defer seeder.Stop()
	seeder.AddTorrent(layout.Metainfo)
	leecher := torrent.Client{
		DataDir:          filepath.Join(layout.BaseDir, "download"),
		DownloadStrategy: &torrent.ResponsiveDownloadStrategy{},
	}
	leecher.Start()
	defer leecher.Stop()
	leecher.AddTorrent(layout.Metainfo)
	leecher.AddPeers(torrent.BytesInfoHash(layout.Metainfo.InfoHash), []torrent.Peer{func() torrent.Peer {
		tcpAddr := seeder.Listener.Addr().(*net.TCPAddr)
		return torrent.Peer{
			IP:   tcpAddr.IP,
			Port: tcpAddr.Port,
		}
	}()})
	fs := New(&leecher)
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
		fuse.Unmount(mountDir)
	}()
	content, err := ioutil.ReadFile(filepath.Join(mountDir, "greeting"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != testutil.GreetingFileContents {
		t.FailNow()
	}
}
