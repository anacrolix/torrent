package torrent

import (
	"cmp"
	"io"
	"os"
	"testing"
	"testing/iotest"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/missinggo/v2"
	"github.com/go-quicktest/qt"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestPeerConnEstablished(t *testing.T) {
	var expectedPeerId PeerID
	missinggo.CopyExact(&expectedPeerId, "12345123451234512345")

	gotPeerConnectedEvt := false
	// Disconnect occurs asynchronously to Client/Torrent lifetime.
	var gotPeerDisconnectedEvt chansync.SetOnce

	ps := testClientTransferParams{
		ConfigureSeeder: ConfigureClient{
			Config: func(cfg *ClientConfig) {
				cfg.PeerID = "12345123451234512345"
			},
		},
		ConfigureLeecher: ConfigureClient{
			Config: func(cfg *ClientConfig) {
				// cfg.DisableUTP = true
				cfg.DisableTCP = true
				cfg.Debug = false
				cfg.DisableTrackers = true
				cfg.EstablishedConnsPerTorrent = 1
				cfg.Callbacks.StatusUpdated = append(
					cfg.Callbacks.StatusUpdated,
					func(e StatusUpdatedEvent) {
						if e.Event == PeerConnected {
							gotPeerConnectedEvt = true
							qt.Assert(t, qt.Equals(e.PeerId, expectedPeerId))
							qt.Assert(t, qt.IsNil(e.Error))
						}
					},
					func(e StatusUpdatedEvent) {
						if e.Event == PeerDisconnected {
							qt.Assert(t, qt.Equals(e.PeerId, expectedPeerId))
							qt.Assert(t, qt.IsNil(e.Error))
							// Signal after checking the values.
							gotPeerDisconnectedEvt.Set()
						}
					},
				)
			},
		},
	}

	testClientTransfer(t, ps)
	// double check that the callbacks were called
	qt.Assert(t, qt.IsTrue(gotPeerConnectedEvt))
	<-gotPeerDisconnectedEvt.Done()
}

type ConfigureClient struct {
	Config func(cfg *ClientConfig)
	Client func(cl *Client)
}

type testClientTransferParams struct {
	SeederUploadRateLimiter    *rate.Limiter
	LeecherDownloadRateLimiter *rate.Limiter
	ConfigureSeeder            ConfigureClient
	ConfigureLeecher           ConfigureClient

	LeecherStartsWithoutMetadata bool
}

// Simplified version of testClientTransfer found in test/leecher-storage.go.
// Could not import and reuse that function due to circular dependencies between modules.
func testClientTransfer(t *testing.T, ps testClientTransferParams) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	// Create seeder and a Torrent.
	cfg := TestingConfig(t)
	cfg.Seed = true
	// Some test instances don't like this being on, even when there's no cache involved.
	cfg.DropMutuallyCompletePeers = false
	cfg.UploadRateLimiter = cmp.Or(ps.SeederUploadRateLimiter, cfg.UploadRateLimiter)
	cfg.DataDir = greetingTempDir
	if ps.ConfigureSeeder.Config != nil {
		ps.ConfigureSeeder.Config(cfg)
	}
	seeder, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	if ps.ConfigureSeeder.Client != nil {
		ps.ConfigureSeeder.Client(seeder)
	}
	seederTorrent, _, _ := seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	defer seeder.Close()
	<-seederTorrent.Complete().On()

	// Create leecher and a Torrent.
	leecherDataDir := t.TempDir()
	cfg = TestingConfig(t)
	// See the seeder client config comment.
	cfg.DropMutuallyCompletePeers = false
	cfg.DataDir = leecherDataDir
	if ps.LeecherDownloadRateLimiter != nil {
		cfg.DownloadRateLimiter = ps.LeecherDownloadRateLimiter
	}
	cfg.Seed = false
	if ps.ConfigureLeecher.Config != nil {
		ps.ConfigureLeecher.Config(cfg)
	}
	leecher, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer leecher.Close()
	if ps.ConfigureLeecher.Client != nil {
		ps.ConfigureLeecher.Client(leecher)
	}
	leecherTorrent, new, err := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		if ps.LeecherStartsWithoutMetadata {
			ret.InfoBytes = nil
		}
		return
	}())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(leecherTorrent.Complete().Bool()))
	qt.Check(t, qt.IsTrue(new))

	added := leecherTorrent.AddClientPeer(seeder)
	qt.Check(t, qt.IsFalse(leecherTorrent.Seeding()))
	// The leecher will use peers immediately if it doesn't have the metadata. Otherwise, they
	// should be sitting idle until we demand data.
	if !ps.LeecherStartsWithoutMetadata {
		qt.Check(t, qt.Equals(leecherTorrent.Stats().PendingPeers, added))
	}
	if ps.LeecherStartsWithoutMetadata {
		<-leecherTorrent.GotInfo()
	}
	r := leecherTorrent.NewReader()
	defer r.Close()
	go leecherTorrent.SetInfoBytes(mi.InfoBytes)

	assertReadAllGreeting(t, r)
	<-leecherTorrent.Complete().On()
	qt.Check(t, qt.Not(qt.HasLen(seederTorrent.PeerConns(), 0)))
	leecherPeerConns := leecherTorrent.PeerConns()
	if cfg.DropMutuallyCompletePeers {
		// I don't think we can assume it will be empty already, due to timing.
		// qt.Check(t, qt.HasLen(leecherPeerConns, 0))
	} else {
		qt.Check(t, qt.Not(qt.HasLen(leecherPeerConns, 0)))
	}
	foundSeeder := false
	for _, pc := range leecherPeerConns {
		completed := pc.PeerPieces().GetCardinality()
		t.Logf("peer conn %v has %v completed pieces", pc, completed)
		if completed == uint64(leecherTorrent.Info().NumPieces()) {
			foundSeeder = true
		}
	}
	if !foundSeeder {
		t.Errorf("didn't find seeder amongst leecher peer conns")
	}

	seederStats := seederTorrent.Stats()
	qt.Check(t, qt.IsTrue(13 <= seederStats.BytesWrittenData.Int64()))
	qt.Check(t, qt.IsTrue(8 <= seederStats.ChunksWritten.Int64()))

	leecherStats := leecherTorrent.Stats()
	qt.Check(t, qt.IsTrue(13 <= leecherStats.BytesReadData.Int64()))
	qt.Check(t, qt.IsTrue(8 <= leecherStats.ChunksRead.Int64()))

	// Try reading through again for the cases where the torrent data size
	// exceeds the size of the cache.
	assertReadAllGreeting(t, r)
}

func assertReadAllGreeting(t *testing.T, r io.ReadSeeker) {
	pos, err := r.Seek(0, io.SeekStart)
	qt.Check(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(pos, 0))
	qt.Check(t, qt.IsNil(iotest.TestReader(r, []byte(testutil.GreetingFileContents))))
}
