package torrentfs

import (
	"bytes"
	"io/ioutil"
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

func TestDownloadOnDemand(t *testing.T) {
	dir, err := ioutil.TempDir("", "torrentfs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	}()
	t.Logf("test directory: %s", dir)
	finishedDir := filepath.Join(dir, "finished")
	os.Mkdir(finishedDir, 0777)
	name := testutil.CreateDummyTorrentData(finishedDir)
	metaInfoBuf := &bytes.Buffer{}
	testutil.CreateMetaInfo(name, metaInfoBuf)
	metaInfo, err := metainfo.Load(metaInfoBuf)
	seeder := torrent.Client{
		DataDir: finishedDir,
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
	seeder.AddTorrent(metaInfo)
	leecher := torrent.Client{
		DataDir: filepath.Join(dir, "download"),
	}
	leecher.Start()
	defer leecher.Stop()
	leecher.AddTorrent(metaInfo)
	leecher.AddPeers(torrent.BytesInfoHash(metaInfo.InfoHash), []torrent.Peer{func() torrent.Peer {
		tcpAddr := seeder.Listener.Addr().(*net.TCPAddr)
		return torrent.Peer{
			IP:   tcpAddr.IP,
			Port: tcpAddr.Port,
		}
	}()})
	mountDir := filepath.Join(dir, "mnt")
	os.Mkdir(mountDir, 0777)
	fs := New(&leecher)
	fuseConn, err := fuse.Mount(mountDir)
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
