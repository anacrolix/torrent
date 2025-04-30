package test

import (
	"io"
	"os"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/filecache"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

type fileCacheClientStorageFactoryParams struct {
	Capacity    int64
	SetCapacity bool
}

func newFileCacheClientStorageFactory(ps fileCacheClientStorageFactoryParams) StorageFactory {
	return func(dataDir string) storage.ClientImplCloser {
		fc, err := filecache.NewCache(dataDir)
		if err != nil {
			panic(err)
		}
		var capFuncPtr storage.TorrentCapacity
		if ps.SetCapacity {
			fc.SetCapacity(ps.Capacity)
			f := func() (cap int64, capped bool) {
				return ps.Capacity, ps.SetCapacity
			}
			capFuncPtr = &f
		}

		return struct {
			storage.ClientImpl
			io.Closer
		}{
			storage.NewResourcePiecesOpts(
				fc.AsResourceProvider(),
				storage.ResourcePiecesOpts{
					Capacity: capFuncPtr,
				}),
			io.NopCloser(nil),
		}
	}
}

func TestClientTransferDefault(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage: newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{}),
	})
}

func TestClientTransferDefaultNoMetadata(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage:               newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{}),
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
		ConfigureSeeder: ConfigureClient{
			Config: func(cfg *torrent.ClientConfig) {
				// If we send too many keep alives, we consume all the leechers available download
				// rate. The default isn't exposed, but a minute is pretty reasonable.
				cfg.KeepAliveTimeout = time.Minute
			},
		},
	})
}

func testClientTransferSmallCache(t *testing.T, setReadahead bool, readahead int64) {
	testClientTransfer(t, testClientTransferParams{
		LeecherStorage: newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{
			SetCapacity: true,
			// Going below the piece length means it can't complete a piece so
			// that it can be hashed.
			Capacity: 5,
		}),
		LeecherStorageCapacity: 5,
		SetReadahead:           setReadahead,
		// Can't readahead too far or the cache will thrash and drop data we
		// thought we had.
		Readahead: readahead,

		// These tests don't work well with more than 1 connection to the seeder.
		ConfigureLeecher: ConfigureClient{
			Config: func(cfg *torrent.ClientConfig) {
				cfg.DropDuplicatePeerIds = true
				// cfg.DisableIPv6 = true
				// cfg.DisableUTP = true
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

func TestFilecacheClientTransferVarious(t *testing.T) {
	TestLeecherStorage(t, LeecherStorageTestCase{
		"Filecache", newFileCacheClientStorageFactory(fileCacheClientStorageFactoryParams{}), 0,
	})
}

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func testSeedAfterDownloading(t *testing.T, disableUtp bool) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)

	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.MaxAllocPeerRequestDataPerConn = 4
	cfg.DataDir = greetingTempDir
	cfg.DisableUTP = disableUtp
	seeder, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s", t)()
	seederTorrent, ok, err := seeder.AddTorrentSpec(torrent.TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	assert.True(t, ok)
	seederTorrent.VerifyData()

	cfg = torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = t.TempDir()
	cfg.DisableUTP = disableUtp
	// Make sure the leecher-leecher doesn't connect directly to the seeder. This is because I
	// wanted to see if having the higher chunk-sized leecher-leecher would cause the leecher to
	// error decoding. However it shouldn't because a client should only be receiving pieces sized
	// to the chunk size it expects.
	cfg.DisablePEX = true
	//cfg.Debug = true
	cfg.Logger = log.Default.WithContextText("leecher")
	leecher, err := torrent.NewClient(cfg)
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l", t)()

	cfg = torrent.TestingConfig(t)
	cfg.DisableUTP = disableUtp
	cfg.Seed = false
	cfg.DataDir = t.TempDir()
	cfg.MaxAllocPeerRequestDataPerConn = 4
	cfg.Logger = log.Default.WithContextText("leecher-leecher")
	cfg.Debug = true
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
	{
		// Prioritize a region, and ensure it's been hashed, so we want connections.
		r := llg.NewReader()
		llg.VerifyData()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer r.Close()
			qt.Check(t, iotest.TestReader(r, []byte(testutil.GreetingFileContents)), qt.IsNil)
		}()
	}
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

func TestSeedAfterDownloadingDisableUtp(t *testing.T) {
	testSeedAfterDownloading(t, true)
}

func TestSeedAfterDownloadingAllowUtp(t *testing.T) {
	testSeedAfterDownloading(t, false)
}
