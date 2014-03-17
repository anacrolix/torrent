package torrentfs

import (
	"bitbucket.org/anacrolix/go.torrent"
	"bytes"
	metainfo "github.com/nsf/libtorgo/torrent"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"testing"
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

func createDummyTorrentData(dirName string) string {
	f, _ := os.Create(filepath.Join(dirName, "greeting"))
	f.WriteString("hello, world\n")
	return f.Name()
}

func createMetaInfo(name string, w io.Writer) {
	builder := metainfo.Builder{}
	builder.AddFile(name)
	builder.AddAnnounceGroup([]string{"lol://cheezburger"})
	batch, err := builder.Submit()
	if err != nil {
		panic(err)
	}
	errs, _ := batch.Start(w, 1)
	<-errs
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
	name := createDummyTorrentData(finishedDir)
	metaInfoBuf := &bytes.Buffer{}
	createMetaInfo(name, metaInfoBuf)
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
	seeder.Start()
	seeder.AddTorrent(metaInfo)
	leecher := torrent.Client{
		DataDir:   filepath.Join(dir, "download"),
		DataReady: make(chan torrent.DataSpec),
	}
	leecher.Start()
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
	err = MountAndServe(mountDir, &leecher)
	if err != nil {
		t.Fatal(err)
	}
}
