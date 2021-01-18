package test

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/missinggo/v2/filecache"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
	sqliteStorage "github.com/anacrolix/torrent/storage/sqlite"
	"golang.org/x/time/rate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testClientTransferParams struct {
	Responsive                 bool
	Readahead                  int64
	SetReadahead               bool
	LeecherStorage             func(string) storage.ClientImplCloser
	SeederStorage              func(string) storage.ClientImplCloser
	SeederUploadRateLimiter    *rate.Limiter
	LeecherDownloadRateLimiter *rate.Limiter
	ConfigureSeeder            ConfigureClient
	ConfigureLeecher           ConfigureClient

	LeecherStartsWithoutMetadata bool
}

func assertReadAllGreeting(t *testing.T, r io.ReadSeeker) {
	pos, err := r.Seek(0, io.SeekStart)
	assert.NoError(t, err)
	assert.EqualValues(t, 0, pos)
	_greeting, err := ioutil.ReadAll(r)
	assert.NoError(t, err)
	assert.EqualValues(t, testutil.GreetingFileContents, string(_greeting))
}

// Creates a seeder and a leecher, and ensures the data transfers when a read
// is attempted on the leecher.
func testClientTransfer(t *testing.T, ps testClientTransferParams) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	// Create seeder and a Torrent.
	cfg := torrent.TestingConfig()
	cfg.Seed = true
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
	seederTorrent.VerifyData()
	// Create leecher and a Torrent.
	leecherDataDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg = torrent.TestingConfig()
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
	assert.NotEmpty(t, seederTorrent.PeerConns())
	leecherPeerConns := leecherTorrent.PeerConns()
	if cfg.DropMutuallyCompletePeers {
		// I don't think we can assume it will be empty already, due to timing.
		//assert.Empty(t, leecherPeerConns)
	} else {
		assert.NotEmpty(t, leecherPeerConns)
	}
	foundSeeder := false
	for _, pc := range leecherPeerConns {
		completed := pc.PeerPieces().Len()
		t.Logf("peer conn %v has %v completed pieces", pc, completed)
		if completed == leecherTorrent.Info().NumPieces() {
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

type fileCacheClientStorageFactoryParams struct {
	Capacity    int64
	SetCapacity bool
	Wrapper     func(*filecache.Cache) storage.ClientImplCloser
}

func newFileCacheClientStorageFactory(ps fileCacheClientStorageFactoryParams) storageFactory {
	return func(dataDir string) storage.ClientImplCloser {
		fc, err := filecache.NewCache(dataDir)
		if err != nil {
			panic(err)
		}
		if ps.SetCapacity {
			fc.SetCapacity(ps.Capacity)
		}
		return ps.Wrapper(fc)
	}
}

type storageFactory func(string) storage.ClientImplCloser

func TestClientTransferDefault(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage: newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{
			Wrapper: fileCachePieceResourceStorage,
		}),
	})
}

func TestClientTransferDefaultNoMetadata(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage: newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{
			Wrapper: fileCachePieceResourceStorage,
		}),
		LeecherStartsWithoutMetadata: true,
	})
}

func TestClientTransferRateLimitedUpload(t *testing.T) {
	started := time.Now()
	testClientTransfer(t, testClientTransferParams{
		// We are uploading 13 bytes (the length of the greeting torrent). The
		// chunks are 2 bytes in length. Then the smallest burst we can run
		// with is 2. Time taken is (13-burst)/rate.
		SeederUploadRateLimiter: rate.NewLimiter(11, 2),
	})
	require.True(t, time.Since(started) > time.Second)
}

func TestClientTransferRateLimitedDownload(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherDownloadRateLimiter: rate.NewLimiter(512, 512),
	})
}

func fileCachePieceResourceStorage(fc *filecache.Cache) storage.ClientImplCloser {
	return struct {
		storage.ClientImpl
		io.Closer
	}{
		storage.NewResourcePieces(fc.AsResourceProvider()),
		ioutil.NopCloser(nil),
	}
}

func testClientTransferSmallCache(t *testing.T, setReadahead bool, readahead int64) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage: newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{
			SetCapacity: true,
			// Going below the piece length means it can't complete a piece so
			// that it can be hashed.
			Capacity: 5,
			Wrapper:  fileCachePieceResourceStorage,
		}),
		SetReadahead: setReadahead,
		// Can't readahead too far or the cache will thrash and drop data we
		// thought we had.
		Readahead: readahead,

		// These tests don't work well with more than 1 connection to the seeder.
		ConfigureLeecher: ConfigureClient{
			Config: func(cfg *torrent.ClientConfig) {
				cfg.DropDuplicatePeerIds = true
				//cfg.DisableIPv6 = true
				//cfg.DisableUTP = true
			},
		},
	})
}

func TestClientTransferSmallCachePieceSizedReadahead(t *testing.T) {
	testClientTransferSmallCache(t, true, 5)
}

func TestClientTransferSmallCacheLargeReadahead(t *testing.T) {
	testClientTransferSmallCache(t, true, 15)
}

func TestClientTransferSmallCacheDefaultReadahead(t *testing.T) {
	testClientTransferSmallCache(t, false, -1)
}

func sqliteClientStorageFactory(connOptsMaker func(dataDir string) sqliteStorage.NewPoolOpts) storageFactory {
	return func(dataDir string) storage.ClientImplCloser {
		connOpts := connOptsMaker(dataDir)
		//log.Printf("opening sqlite db: %#v", connOpts)
		ret, err := sqliteStorage.NewPiecesStorage(sqliteStorage.NewPiecesStorageOpts{NewPoolOpts: connOpts})
		if err != nil {
			panic(err)
		}
		return ret
	}
}

func TestClientTransferVarious(t *testing.T) {
	// Leecher storage
	for _, ls := range []struct {
		name string
		f    storageFactory
	}{
		{"Filecache", newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{
			Wrapper: fileCachePieceResourceStorage,
		})},
		{"Boltdb", storage.NewBoltDB},
		{"SqliteFile", sqliteClientStorageFactory(func(dataDir string) sqliteStorage.NewPoolOpts {
			return sqliteStorage.NewPoolOpts{
				Path: filepath.Join(dataDir, "sqlite.db"),
			}
		})},
		{"SqliteMemory", sqliteClientStorageFactory(func(dataDir string) sqliteStorage.NewPoolOpts {
			return sqliteStorage.NewPoolOpts{
				Memory: true,
			}
		})},
	} {
		t.Run(fmt.Sprintf("LeecherStorage=%s", ls.name), func(t *testing.T) {
			// Seeder storage
			for _, ss := range []struct {
				name string
				f    storageFactory
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
									LeecherStorage: ls.f,
								})
							})
							for _, readahead := range []int64{-1, 0, 1, 2, 3, 4, 5, 6, 9, 10, 11, 12, 13, 14, 15, 20} {
								t.Run(fmt.Sprintf("readahead=%v", readahead), func(t *testing.T) {
									testClientTransfer(t, testClientTransferParams{
										SeederStorage:  ss.f,
										Responsive:     responsive,
										SetReadahead:   true,
										Readahead:      readahead,
										LeecherStorage: ls.f,
									})
								})
							}
						})
					}
				})
			}
		})
	}
}

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func TestSeedAfterDownloading(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	cfg := torrent.TestingConfig()
	cfg.Seed = true
	cfg.DataDir = greetingTempDir
	seeder, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s", t)()
	seederTorrent, ok, err := seeder.AddTorrentSpec(torrent.TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, ok)
	seederTorrent.VerifyData()

	cfg = torrent.TestingConfig()
	cfg.Seed = true
	cfg.DataDir, err = ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecher, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l", t)()

	cfg = torrent.TestingConfig()
	cfg.Seed = false
	cfg.DataDir, err = ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecherLeecher, _ := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer leecherLeecher.Close()
	defer testutil.ExportStatusWriter(leecherLeecher, "ll", t)()
	leecherGreeting, ok, err := leecher.AddTorrentSpec(func() (ret *torrent.TorrentSpec) {
		ret = torrent.TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	require.NoError(t, err)
	assert.True(t, ok)
	llg, ok, err := leecherLeecher.AddTorrentSpec(func() (ret *torrent.TorrentSpec) {
		ret = torrent.TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 3
		return
	}())
	require.NoError(t, err)
	assert.True(t, ok)
	// Simultaneously DownloadAll in Leecher, and read the contents
	// consecutively in LeecherLeecher. This non-deterministically triggered a
	// case where the leecher wouldn't unchoke the LeecherLeecher.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := llg.NewReader()
		defer r.Close()
		b, err := ioutil.ReadAll(r)
		require.NoError(t, err)
		assert.EqualValues(t, testutil.GreetingFileContents, b)
	}()
	done := make(chan struct{})
	defer close(done)
	go leecherGreeting.AddClientPeer(seeder)
	go leecherGreeting.AddClientPeer(leecherLeecher)
	wg.Add(1)
	go func() {
		defer wg.Done()
		leecherGreeting.DownloadAll()
		leecher.WaitAll()
	}()
	wg.Wait()
}

type ConfigureClient struct {
	Config func(*torrent.ClientConfig)
	Client func(*torrent.Client)
}
