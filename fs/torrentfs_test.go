package torrentfs

import (
	"context"
	netContext "context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	log.SetFlags(log.Flags() | log.Lshortfile)
}

func TestTCPAddrString(t *testing.T) {
	l, err := net.Listen("tcp4", "localhost:0")
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
	ta := &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: missinggo.AddrPort(l.Addr()),
	}
	s := ta.String()
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

func (tl *testLayout) Destroy() error {
	return os.RemoveAll(tl.BaseDir)
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
	testutil.CreateDummyTorrentData(tl.Completed)
	tl.Metainfo = testutil.GreetingMetaInfo()
	return
}

// Unmount without first killing the FUSE connection while there are FUSE
// operations blocked inside the filesystem code.
func TestUnmountWedged(t *testing.T) {
	layout, err := newGreetingLayout()
	require.NoError(t, err)
	defer func() {
		err := layout.Destroy()
		if err != nil {
			t.Log(err)
		}
	}()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = filepath.Join(layout.BaseDir, "incomplete")
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	client, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()
	tt, err := client.AddTorrent(layout.Metainfo)
	require.NoError(t, err)
	fs := New(client)
	fuseConn, err := fuse.Mount(layout.MountDir)
	if err != nil {
		switch err.Error() {
		case "cannot locate OSXFUSE":
			fallthrough
		case "fusermount: exit status 1":
			t.Skip(err)
		}
		t.Fatal(err)
	}
	go func() {
		server := fusefs.New(fuseConn, &fusefs.Config{
			Debug: func(msg interface{}) {
				t.Log(msg)
			},
		})
		server.Serve(fs)
	}()
	<-fuseConn.Ready
	if err := fuseConn.MountError; err != nil {
		t.Fatalf("mount error: %s", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Read the greeting file, though it will never be available. This should
	// "wedge" FUSE, requiring the fs object to be forcibly destroyed. The
	// read call will return with a FS error.
	go func() {
		<-ctx.Done()
		fs.mu.Lock()
		fs.event.Broadcast()
		fs.mu.Unlock()
	}()
	go func() {
		defer cancel()
		_, err := ioutil.ReadFile(filepath.Join(layout.MountDir, tt.Info().Name))
		require.Error(t, err)
	}()

	// Wait until the read has blocked inside the filesystem code.
	fs.mu.Lock()
	for fs.blockedReads != 1 && ctx.Err() == nil {
		fs.event.Wait()
	}
	fs.mu.Unlock()

	fs.Destroy()

	for {
		err = fuse.Unmount(layout.MountDir)
		if err != nil {
			t.Logf("error unmounting: %s", err)
			time.Sleep(time.Millisecond)
		} else {
			break
		}
	}

	err = fuseConn.Close()
	assert.NoError(t, err)
}

func TestDownloadOnDemand(t *testing.T) {
	layout, err := newGreetingLayout()
	require.NoError(t, err)
	defer layout.Destroy()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = layout.Completed
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.Seed = true
	cfg.ListenPort = 0
	cfg.ListenHost = torrent.LoopbackListenHost
	seeder, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s")()
	// Just to mix things up, the seeder starts with the data, but the leecher
	// starts with the metainfo.
	seederTorrent, err := seeder.AddMagnet(fmt.Sprintf("magnet:?xt=urn:btih:%s", layout.Metainfo.HashInfoBytes().HexString()))
	require.NoError(t, err)
	go func() {
		// Wait until we get the metainfo, then check for the data.
		<-seederTorrent.GotInfo()
		seederTorrent.VerifyData()
	}()
	cfg = torrent.NewDefaultClientConfig()
	cfg.DisableTrackers = true
	cfg.NoDHT = true
	cfg.DisableTCP = true
	cfg.DefaultStorage = storage.NewMMap(filepath.Join(layout.BaseDir, "download"))
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	leecher, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	testutil.ExportStatusWriter(leecher, "l")()
	defer leecher.Close()
	leecherTorrent, err := leecher.AddTorrent(layout.Metainfo)
	require.NoError(t, err)
	leecherTorrent.AddClientPeer(seeder)
	fs := New(leecher)
	defer fs.Destroy()
	root, _ := fs.Root()
	node, _ := root.(fusefs.NodeStringLookuper).Lookup(netContext.Background(), "greeting")
	var attr fuse.Attr
	node.Attr(netContext.Background(), &attr)
	size := attr.Size
	resp := &fuse.ReadResponse{
		Data: make([]byte, size),
	}
	h, err := node.(fusefs.NodeOpener).Open(context.TODO(), nil, nil)
	require.NoError(t, err)
	h.(fusefs.HandleReader).Read(netContext.Background(), &fuse.ReadRequest{
		Size: int(size),
	}, resp)
	assert.EqualValues(t, testutil.GreetingFileContents, resp.Data)
}

func TestIsSubPath(t *testing.T) {
	for _, case_ := range []struct {
		parent, child string
		is            bool
	}{
		{"", "", false},
		{"", "/", true},
		{"", "a", true},
		{"a/b", "a/bc", false},
		{"a/b", "a/b", false},
		{"a/b", "a/b/c", true},
		{"a/b", "a//b", false},
	} {
		assert.Equal(t, case_.is, isSubPath(case_.parent, case_.child))
	}
}
