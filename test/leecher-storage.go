package test

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"testing/iotest"

	"github.com/anacrolix/missinggo/v2/bitmap"
	qt "github.com/go-quicktest/qt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

type LeecherStorageTestCase struct {
	Name       string
	Factory    StorageFactory
	GoMaxProcs int
}

type StorageFactory func(string) storage.ClientImplCloser

func TestLeecherStorage(t *testing.T, ls LeecherStorageTestCase) {
	// Seeder storage
	for _, ss := range []struct {
		name string
		f    StorageFactory
	}{
		{"File", storage.NewFile},
		{"Mmap", storage.NewMMap},
	} {
		t.Run(fmt.Sprintf("%sSeederStorage", ss.name), func(t *testing.T) {
			for _, responsive := range []bool{false, true} {
				t.Run(fmt.Sprintf("Responsive=%v", responsive), func(t *testing.T) {
					t.Run("NoReadahead", func(t *testing.T) {
						testClientTransfer(t, testClientTransferParams{
							Responsive:     responsive,
							SeederStorage:  ss.f,
							LeecherStorage: ls.Factory,
							GOMAXPROCS:     ls.GoMaxProcs,
						})
					})
					for _, readahead := range []int64{-1, 0, 1, 2, 9, 20} {
						t.Run(fmt.Sprintf("readahead=%v", readahead), func(t *testing.T) {
							testClientTransfer(t, testClientTransferParams{
								SeederStorage:  ss.f,
								Responsive:     responsive,
								SetReadahead:   true,
								Readahead:      readahead,
								LeecherStorage: ls.Factory,
								GOMAXPROCS:     ls.GoMaxProcs,
							})
						})
					}
				})
			}
		})
	}
}

type ConfigureClient struct {
	Config func(cfg *torrent.ClientConfig)
	Client func(cl *torrent.Client)
}

type testClientTransferParams struct {
	Responsive     bool
	Readahead      int64
	SetReadahead   bool
	LeecherStorage func(string) storage.ClientImplCloser
	// TODO: Use a generic option type. This is the capacity of the leecher storage for determining
	// whether it's possible for the leecher to be Complete. 0 currently means no limit.
	LeecherStorageCapacity     int64
	SeederStorage              func(string) storage.ClientImplCloser
	SeederUploadRateLimiter    *rate.Limiter
	LeecherDownloadRateLimiter *rate.Limiter
	ConfigureSeeder            ConfigureClient
	ConfigureLeecher           ConfigureClient
	GOMAXPROCS                 int

	LeecherStartsWithoutMetadata bool
}

// Creates a seeder and a leecher, and ensures the data transfers when a read
// is attempted on the leecher.
func testClientTransfer(t *testing.T, ps testClientTransferParams) {
	prevGOMAXPROCS := runtime.GOMAXPROCS(ps.GOMAXPROCS)
	newGOMAXPROCS := prevGOMAXPROCS
	if ps.GOMAXPROCS > 0 {
		newGOMAXPROCS = ps.GOMAXPROCS
	}
	defer func() {
		qt.Check(t, qt.ContentEquals(runtime.GOMAXPROCS(prevGOMAXPROCS), newGOMAXPROCS))
	}()

	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	// Create seeder and a Torrent.
	cfg := torrent.TestingConfig(t)
	// cfg.Debug = true
	cfg.Seed = true
	// Less than a piece, more than a single request.
	cfg.MaxAllocPeerRequestDataPerConn = 4
	// Some test instances don't like this being on, even when there's no cache involved.
	cfg.DropMutuallyCompletePeers = false
	if ps.SeederUploadRateLimiter != nil {
		cfg.UploadRateLimiter = ps.SeederUploadRateLimiter
	}
	// cfg.ListenAddr = "localhost:4000"
	if ps.SeederStorage != nil {
		storage := ps.SeederStorage(greetingTempDir)
		defer storage.Close()
		cfg.DefaultStorage = storage
	} else {
		cfg.DataDir = greetingTempDir
	}
	if ps.ConfigureSeeder.Config != nil {
		ps.ConfigureSeeder.Config(cfg)
	}
	seeder, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	if ps.ConfigureSeeder.Client != nil {
		ps.ConfigureSeeder.Client(seeder)
	}
	defer testutil.ExportStatusWriter(seeder, "s", t)()
	seederTorrent, _, _ := seeder.AddTorrentSpec(torrent.TorrentSpecFromMetaInfo(mi))
	// Run a Stats right after Closing the Client. This will trigger the Stats
	// panic in #214 caused by RemoteAddr on Closed uTP sockets.
	defer seederTorrent.Stats()
	defer seeder.Close()
	// Adding a torrent and setting the info should trigger piece checks for everything
	// automatically. Wait until the seed Torrent agrees that everything is available.
	<-seederTorrent.Complete().On()
	// Create leecher and a Torrent.
	leecherDataDir := t.TempDir()
	cfg = torrent.TestingConfig(t)
	// See the seeder client config comment.
	cfg.DropMutuallyCompletePeers = false
	if ps.LeecherStorage == nil {
		cfg.DataDir = leecherDataDir
	} else {
		storage := ps.LeecherStorage(leecherDataDir)
		defer storage.Close()
		cfg.DefaultStorage = storage
	}
	if ps.LeecherDownloadRateLimiter != nil {
		cfg.DownloadRateLimiter = ps.LeecherDownloadRateLimiter
	}
	cfg.Seed = false
	// cfg.Debug = true
	if ps.ConfigureLeecher.Config != nil {
		ps.ConfigureLeecher.Config(cfg)
	}
	leecher, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer leecher.Close()
	if ps.ConfigureLeecher.Client != nil {
		ps.ConfigureLeecher.Client(leecher)
	}
	defer testutil.ExportStatusWriter(leecher, "l", t)()
	leecherTorrent, new, err := leecher.AddTorrentSpec(func() (ret *torrent.TorrentSpec) {
		ret = torrent.TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		if ps.LeecherStartsWithoutMetadata {
			ret.InfoBytes = nil
		}
		return
	}())
	require.NoError(t, err)
	assert.False(t, leecherTorrent.Complete().Bool())
	assert.True(t, new)

	//// This was used when observing coalescing of piece state changes.
	//logPieceStateChanges(leecherTorrent)

	// Now do some things with leecher and seeder.
	added := leecherTorrent.AddClientPeer(seeder)
	assert.False(t, leecherTorrent.Seeding())
	// The leecher will use peers immediately if it doesn't have the metadata. Otherwise, they
	// should be sitting idle until we demand data.
	if !ps.LeecherStartsWithoutMetadata {
		assert.EqualValues(t, added, leecherTorrent.Stats().PendingPeers)
	}
	if ps.LeecherStartsWithoutMetadata {
		<-leecherTorrent.GotInfo()
	}
	r := leecherTorrent.NewReader()
	defer r.Close()
	go leecherTorrent.SetInfoBytes(mi.InfoBytes)
	if ps.Responsive {
		r.SetResponsive()
	}
	if ps.SetReadahead {
		r.SetReadahead(ps.Readahead)
	}
	assertReadAllGreeting(t, r)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	canComplete := ps.LeecherStorageCapacity == 0 || ps.LeecherStorageCapacity >= info.TotalLength()
	if !canComplete {
		// Reading from a cache doesn't refresh older pieces until we fail to read those, so we need
		// to force a refresh since we just read the contents from start to finish.
		go leecherTorrent.VerifyData()
	}
	if canComplete {
		<-leecherTorrent.Complete().On()
	} else {
		<-leecherTorrent.Complete().Off()
	}
	assert.NotEmpty(t, seederTorrent.PeerConns())
	leecherPeerConns := leecherTorrent.PeerConns()
	if cfg.DropMutuallyCompletePeers {
		// I don't think we can assume it will be empty already, due to timing.
		// assert.Empty(t, leecherPeerConns)
	} else {
		assert.NotEmpty(t, leecherPeerConns)
	}
	foundSeeder := false
	for _, pc := range leecherPeerConns {
		completed := pc.PeerPieces().GetCardinality()
		t.Logf("peer conn %v has %v completed pieces", pc, completed)
		if completed == bitmap.BitRange(leecherTorrent.Info().NumPieces()) {
			foundSeeder = true
		}
	}
	if !foundSeeder {
		t.Errorf("didn't find seeder amongst leecher peer conns")
	}

	seederStats := seederTorrent.Stats()
	assert.True(t, 13 <= seederStats.BytesWrittenData.Int64())
	assert.True(t, 8 <= seederStats.ChunksWritten.Int64())

	leecherStats := leecherTorrent.Stats()
	assert.True(t, 13 <= leecherStats.BytesReadData.Int64())
	assert.True(t, 8 <= leecherStats.ChunksRead.Int64())

	// Try reading through again for the cases where the torrent data size
	// exceeds the size of the cache.
	assertReadAllGreeting(t, r)
}

func assertReadAllGreeting(t *testing.T, r io.ReadSeeker) {
	pos, err := r.Seek(0, io.SeekStart)
	assert.NoError(t, err)
	assert.EqualValues(t, 0, pos)
	qt.Check(t, qt.IsNil(iotest.TestReader(r, []byte(testutil.GreetingFileContents))))
}
