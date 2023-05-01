package torrent

import (
	"os"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/internal/testutil"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func TestHolepunchConnect(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.MaxAllocPeerRequestDataPerConn = 4
	cfg.DataDir = greetingTempDir
	cfg.DisablePEX = true
	cfg.Debug = true
	cfg.AcceptPeerConnections = false
	//cfg.DisableUTP = true
	seeder, err := NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s", t)()
	seederTorrent, ok, err := seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, ok)
	seederTorrent.VerifyData()

	cfg = TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = t.TempDir()
	cfg.AlwaysWantConns = true
	// This way the leecher leecher will still try to use this peer as a relay, but won't be told
	// about the seeder via PEX.
	//cfg.DisablePEX = true
	//cfg.Debug = true
	leecher, err := NewClient(cfg)
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l", t)()

	cfg = TestingConfig(t)
	cfg.Seed = false
	cfg.DataDir = t.TempDir()
	cfg.MaxAllocPeerRequestDataPerConn = 4
	//cfg.Debug = true
	//cfg.DisableUTP = true
	leecherLeecher, _ := NewClient(cfg)
	require.NoError(t, err)
	defer leecherLeecher.Close()
	defer testutil.ExportStatusWriter(leecherLeecher, "ll", t)()
	leecherGreeting, ok, err := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	_ = leecherGreeting
	require.NoError(t, err)
	assert.True(t, ok)
	llg, ok, err := leecherLeecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 3
		return
	}())
	require.NoError(t, err)
	assert.True(t, ok)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := llg.NewReader()
		defer r.Close()
		qt.Check(t, iotest.TestReader(r, []byte(testutil.GreetingFileContents)), qt.IsNil)
	}()
	go seederTorrent.AddClientPeer(leecher)
	waitForConns(seederTorrent)
	go llg.AddClientPeer(leecher)
	waitForConns(llg)
	//time.Sleep(time.Second)
	llg.cl.lock()
	targetAddr := seeder.ListenAddrs()[1]
	log.Printf("trying to initiate to %v", targetAddr)
	llg.initiateConn(PeerInfo{
		Addr: targetAddr,
	}, true, false)
	llg.cl.unlock()
	wg.Wait()
}

func waitForConns(t *Torrent) {
	t.cl.lock()
	defer t.cl.unlock()
	for {
		for range t.conns {
			return
		}
		t.cl.event.Wait()
	}
}
