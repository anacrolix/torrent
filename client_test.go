package torrent_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/missinggo/v2/filecache"
	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/autobind"
	"github.com/james-lawrence/torrent/connections"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/cryptox"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/md5x"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/james-lawrence/torrent/internal/testx"
	"github.com/james-lawrence/torrent/internal/utpx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/storage"
)

func TestingSeedConfig(t *testing.T, dir string) *torrent.ClientConfig {
	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = dir
	return cfg
}

func TestingLeechConfig(t *testing.T, dir string) *torrent.ClientConfig {
	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = dir
	return cfg
}

func TestClientDefault(t *testing.T) {
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	cl.Close()
}

func TestClientNilConfig(t *testing.T) {
	cl, err := torrent.NewClient(nil)
	require.NoError(t, err)
	cl.Close()
}

func TestBoltPieceCompletionClosedWhenClientClosed(t *testing.T) {
	cfg := torrent.TestingConfig(t)
	ci := storage.NewFile(cfg.DataDir)
	defer ci.Close()

	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	cl.Close()
	// And again, https://github.com/anacrolix/torrent/issues/158
	cl, err = autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	cl.Close()
}

func TestAddDropTorrent(t *testing.T) {
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	dir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(dir)

	tt, added, err := cl.MaybeStart(torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(dir))))
	require.NoError(t, err)
	assert.True(t, added)

	require.NoError(t, tt.Tune(torrent.TuneMaxConnections(0)))
	require.NoError(t, tt.Tune(torrent.TuneMaxConnections(1)))

	cl.Stop(tt.Metadata())
}

func TestAddDropManyTorrents(t *testing.T) {
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	for i := range iter.N(1000) {
		var spec torrent.Metadata
		binary.PutVarint((&spec).InfoHash[:], int64(i))
		_, added, err := cl.Start(spec)
		require.NoError(t, err)
		assert.True(t, added)
		defer cl.Stop(spec)
	}
}

func NewFileCacheClientStorageFactory(dataDir string) storage.ClientImpl {
	return storage.NewFile(dataDir, storage.FileOptionPathMakerInfohashV0)
}

type StorageFactory func(string) storage.ClientImpl

func TestClientTransferRateLimitedUpload(t *testing.T) {
	started := time.Now()
	testClientTransfer(t, testClientTransferParams{
		// We are uploading 13 bytes (the length of the greeting torrent). The
		// chunks are 2 bytes in length. Then the smallest burst we can run
		// with is 2. Time taken is (13-burst)/rate.
		SeederUploadRateLimiter: rate.NewLimiter(11, 2),
		ExportClientStatus:      true,
	})
	require.True(t, time.Since(started) > time.Second)
}

func TestClientTransferRateLimitedDownload(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		LeecherDownloadRateLimiter: rate.NewLimiter(512, 512),
	})
}

func fileCachePieceResourceStorage(fc *filecache.Cache) storage.ClientImpl {
	return storage.NewFile(os.TempDir())
}

func TestClientTransferVarious(t *testing.T) {
	// Leecher storage
	for _, ls := range []StorageFactory{
		func(dir string) storage.ClientImpl {
			return storage.NewFile(dir, storage.FileOptionPathMakerInfohashV0)
		},
		// storage.NewBoltDB,
	} {
		// Seeder storage
		for _, ss := range []func(string) storage.ClientImpl{
			func(dir string) storage.ClientImpl {
				return storage.NewFile(dir)
			},
			storage.NewMMap,
		} {
			testClientTransfer(t, testClientTransferParams{
				SeederStorage:  ss,
				LeecherStorage: ls,
			})
		}
	}
}

type testClientTransferParams struct {
	ExportClientStatus         bool
	LeecherStorage             func(string) storage.ClientImpl
	SeederStorage              func(string) storage.ClientImpl
	SeederUploadRateLimiter    *rate.Limiter
	LeecherDownloadRateLimiter *rate.Limiter
}

func TestClientTransferDefault(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		ExportClientStatus: true,
		LeecherStorage: func(dir string) storage.ClientImpl {
			return storage.NewFile(dir, storage.FileOptionPathMakerInfohashV0)
		},
	})
}

// Creates a seeder and a leecher, and ensures the data transfers when a read
// is attempted on the leecher.
func testClientTransfer(t *testing.T, ps testClientTransferParams) {
	ctx, done := testx.Context(t)
	defer done()

	greetingTempDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(greetingTempDir)
	// Create seeder and a Torrent.
	cfg := torrent.TestingConfig(t)
	cfg.DataDir = greetingTempDir
	cfg.Seed = true
	if ps.SeederUploadRateLimiter != nil {
		cfg.UploadRateLimiter = ps.SeederUploadRateLimiter
	}

	sstore := storage.NewFile(greetingTempDir)
	storageopt := torrent.OptionStorage(sstore)
	if ps.SeederStorage != nil {
		sstore.Close()
		store := ps.SeederStorage(greetingTempDir)
		defer log.Println("closed seeder storage")
		defer store.Close()
		storageopt = torrent.OptionStorage(store)
	}

	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	if ps.ExportClientStatus {
		defer testutil.ExportStatusWriter(seeder, "s")()
	}

	seederTorrent, _, err := seeder.MaybeStart(torrent.NewFromMetaInfo(mi, storageopt))
	require.NoError(t, err)
	// Run a Stats right after Closing the Client. This will trigger the Stats
	// panic in #214 caused by RemoteAddr on Closed uTP sockets.
	defer seederTorrent.Stats()
	defer seeder.Close()
	require.NoError(t, torrent.Verify(ctx, seederTorrent))

	// Create leecher and a Torrent.
	cfg = torrent.TestingConfig(t)
	cfg.DataDir = t.TempDir()
	defer os.RemoveAll(cfg.DataDir)

	lstore := storage.NewFile(cfg.DataDir, storage.FileOptionPathMakerInfohashV0)
	leechstorageopt := torrent.OptionStorage(lstore)
	if ps.LeecherStorage != nil {
		lstore.Close()
		lstore := ps.LeecherStorage(cfg.DataDir)
		defer lstore.Close()
		leechstorageopt = torrent.OptionStorage(lstore)
	}

	if ps.LeecherDownloadRateLimiter != nil {
		cfg.DownloadRateLimiter = ps.LeecherDownloadRateLimiter
	}
	cfg.Seed = false

	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer leecher.Close()
	if ps.ExportClientStatus {
		defer testutil.ExportStatusWriter(leecher, "l")()
	}
	leecherTorrent, added, err := leecher.MaybeStart(
		torrent.NewFromMetaInfo(
			mi,
			torrent.OptionChunk(2),
			leechstorageopt,
		),
	)
	require.NoError(t, err)
	assert.True(t, added)

	// Now do some things with leecher and seeder.
	require.NoError(t, leecherTorrent.Tune(torrent.TuneClientPeer(seeder)))

	// The Torrent should not be interested in obtaining peers, so the one we
	// just added should be the only one.
	assert.False(t, leecherTorrent.Stats().Seeding)

	// begin downloading
	_, err = torrent.DownloadInto(ctx, io.Discard, leecherTorrent)
	require.NoError(t, err)

	r := torrent.NewReader(leecherTorrent)
	defer r.Close()

	assertReadAllGreeting(t, r)

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
	_greeting, err := io.ReadAll(r)
	assert.NoError(t, err)
	assert.EqualValues(t, testutil.GreetingFileContents, string(_greeting))
}

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func TestSeedAfterDownloading(t *testing.T) {
	ctx, _done := testx.Context(t)
	defer _done()

	greetingTempDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(greetingTempDir)

	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = greetingTempDir
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s")()

	seederTorrent, ok, err := seeder.MaybeStart(torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(cfg.DataDir))))
	require.NoError(t, err)
	assert.True(t, ok)

	// TODO: implement verify
	require.NoError(t, torrent.Verify(ctx, seederTorrent))
	require.True(t, seeder.WaitAll())
	// log.Printf("SEEDER %p c(%p)\n", seederTorrent, seederTorrent.(*torrent).piecesM)

	cfg = torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir, err = os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l")()

	cfg = torrent.TestingConfig(t)
	cfg.Seed = false
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecherLeecher, _ := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer leecherLeecher.Close()
	defer testutil.ExportStatusWriter(leecherLeecher, "ll")()
	leecherGreeting, ok, err := leecher.MaybeStart(torrent.NewFromMetaInfo(mi, torrent.OptionChunk(2), torrent.OptionStorage(storage.NewFile(cfg.DataDir))))
	require.NoError(t, err)
	assert.True(t, ok)

	llg, ok, err := leecherLeecher.MaybeStart(torrent.NewFromMetaInfo(mi, torrent.OptionChunk(3), torrent.OptionStorage(storage.NewFile(cfg.DataDir))))
	require.NoError(t, err)
	assert.True(t, ok)

	// Simultaneously DownloadAll in Leecher, and read the contents
	// consecutively in LeecherLeecher. This non-deterministically triggered a
	// case where the leecher wouldn't unchoke the LeecherLeecher.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var buf bytes.Buffer
		_, err = torrent.DownloadInto(ctx, &buf, llg)
		require.NoError(t, err)
		assert.EqualValues(t, testutil.GreetingFileContents, buf.Bytes())
	}()
	done := make(chan struct{})
	defer close(done)
	go leecherGreeting.Tune(torrent.TuneClientPeer(seeder))
	go leecherGreeting.Tune(torrent.TuneClientPeer(leecherLeecher))

	digest := md5.New()
	n, err := torrent.DownloadInto(ctx, digest, leecherGreeting)
	require.Equal(t, int64(13), n)
	require.NoError(t, err)
	require.Equal(t, "22c3683b094136c3398391ae71b20f04", md5x.FormatHex(digest))
}

func TestDownload(t *testing.T) {
	buf := bytes.NewBufferString("")

	greetingTempDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(greetingTempDir)
	metadata, err := torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(greetingTempDir)))
	require.NoError(t, err)

	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(TestingSeedConfig(t, greetingTempDir)))
	require.NoError(t, err)
	defer seeder.Close()
	tor, added, err := seeder.Start(metadata)
	require.NoError(t, err)
	require.True(t, added)
	_, err = torrent.DownloadInto(context.Background(), io.Discard, tor)
	require.NoError(t, err)

	lcfg := TestingLeechConfig(t, t.TempDir())
	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(lcfg))
	require.NoError(t, err)
	defer leecher.Close()
	metadata, err = torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(lcfg.DataDir)))
	require.NoError(t, err)
	ltor, added, err := leecher.Start(metadata)
	require.NoError(t, err)
	require.True(t, added)
	_, err = torrent.DownloadInto(context.Background(), buf, ltor, torrent.TuneClientPeer(seeder))
	require.NoError(t, err)
	require.Equal(t, "hello, world\n", buf.String())
}

func TestDownloadMetadataTimeout(t *testing.T) {
	buf := bytes.NewBufferString("")

	greetingTempDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(greetingTempDir)
	metadata, err := torrent.New(mi.HashInfoBytes())
	require.NoError(t, err)

	cfg := torrent.TestingConfig(t)
	cfg.DataDir, err = os.MkdirTemp("", "")
	require.Nil(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer leecher.Close()
	ctx, done := context.WithTimeout(context.Background(), 0)
	defer done()
	ltor, added, err := leecher.Start(metadata)
	require.NoError(t, err)
	require.True(t, added)
	_, err = torrent.DownloadInto(ctx, buf, ltor)
	require.Equal(t, context.DeadlineExceeded, err)
}

func TestCompletedPieceWrongSize(t *testing.T) {
	cfg := torrent.TestingConfig(t)
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()
	info := metainfo.Info{
		Length:      13,
		PieceLength: 15,
		Pieces:      make([]byte, 20),
	}

	b, err := bencode.Marshal(info)
	require.NoError(t, err)
	ts, err := torrent.New(metainfo.HashBytes(b), torrent.OptionInfo(b), torrent.OptionStorage(testutil.NewBadStorage()))
	require.NoError(t, err)
	tt, new, err := cl.Start(ts)
	require.NoError(t, err)
	defer cl.Stop(ts)
	assert.True(t, new)
	require.Equal(t, 1, tt.Stats().Missing)
}

func BenchmarkAddLargeTorrent(b *testing.B) {
	cfg := torrent.TestingConfig(b)
	cl, err := autobind.NewLoopback(
		autobind.DisableTCP,
		autobind.DisableUTP,
	).Bind(torrent.NewClient(cfg))
	require.NoError(b, err)
	defer cl.Close()
	b.ReportAllocs()
	for range iter.N(b.N) {
		t, err := torrent.NewFromMetaInfoFile("testdata/bootstrap.dat.torrent")
		if err != nil {
			b.Fatal(err)
		}

		_, _, err = cl.Start(t)
		if err != nil {
			b.Fatal(err)
		}
		cl.Stop(t)
	}
}

func TestResponsive(t *testing.T) {
	ctx, done := testx.Context(t)
	defer done()

	seederDataDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(seederDataDir)
	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = seederDataDir
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.Nil(t, err)
	defer seeder.Close()
	tt, err := torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(cfg.DataDir)))
	require.Nil(t, err)
	seederTorrent, _, _ := seeder.Start(tt)
	require.NoError(t, torrent.Verify(ctx, seederTorrent))
	leecherDataDir, err := os.MkdirTemp("", "")
	require.Nil(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg = torrent.TestingConfig(t)
	cfg.DataDir = leecherDataDir
	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer leecher.Close()
	tt, err = torrent.NewFromMetaInfo(mi, torrent.OptionChunk(2), torrent.OptionStorage(storage.NewFile(cfg.DataDir)))
	require.NoError(t, err)
	leecherTorrent, _, err := leecher.Start(tt)
	require.NoError(t, err)
	leecherTorrent.Tune(torrent.TuneClientPeer(seeder))

	_, err = torrent.DownloadInto(ctx, io.Discard, leecherTorrent)
	require.NoError(t, err)

	reader := torrent.NewReader(leecherTorrent)
	defer reader.Close()

	b := make([]byte, 2)
	_, err = reader.Seek(3, io.SeekStart)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, "lo", string(b))
	_, err = reader.Seek(11, io.SeekStart)
	require.NoError(t, err)
	n, err := io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, "d\n", string(b))
}

func TestTorrentDroppedDuringResponsiveRead(t *testing.T) {
	ctx, done := testx.Context(t)
	defer done()

	seederDataDir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(seederDataDir)
	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = seederDataDir
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.Nil(t, err)
	defer seeder.Close()
	st, err := torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewFile(cfg.DataDir)))
	require.Nil(t, err)

	seederTorrent, _, _ := seeder.Start(st)
	require.NoError(t, torrent.Verify(ctx, seederTorrent))

	leecherDataDir, err := os.MkdirTemp("", "")
	require.Nil(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg = torrent.TestingConfig(t)
	cfg.DataDir = leecherDataDir
	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.Nil(t, err)
	defer leecher.Close()
	lt, err := torrent.NewFromMetaInfo(mi, torrent.OptionChunk(2), torrent.OptionStorage(storage.NewFile(cfg.DataDir)))
	require.Nil(t, err)
	leecherTorrent, _, _ := leecher.Start(lt)
	leecherTorrent.Tune(torrent.TuneClientPeer(seeder))
	reader := torrent.NewReader(leecherTorrent)
	defer reader.Close()

	b := make([]byte, 2)
	_, err = reader.Seek(3, io.SeekStart)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, "lo", string(b))
	leecher.Stop(lt)
	_, err = reader.Seek(11, io.SeekStart)
	require.NoError(t, err)
	n, err := reader.Read(b)
	assert.EqualError(t, err, "storage closed")
	assert.EqualValues(t, 0, n)
}

// Check that stuff is merged in subsequent Start for the same
// infohash.
func TestAddTorrentMerging(t *testing.T) {
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	dir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(dir)
	ts, err := torrent.NewFromMetaInfo(mi, torrent.OptionDisplayName("foo"), torrent.OptionStorage(storage.NewFile(dir)))

	require.NoError(t, err)
	tt, added, err := cl.Start(ts)
	require.NoError(t, err)
	require.True(t, added)
	assert.Equal(t, "foo", tt.Metadata().DisplayName)
	_, added, err = cl.Start(ts.Merge(torrent.OptionDisplayName("bar")))
	require.NoError(t, err)
	require.False(t, added)
	assert.Equal(t, "bar", tt.Metadata().DisplayName)
}

func TestTorrentDroppedBeforeGotInfo(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent(t)
	os.RemoveAll(dir)
	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	ts, err := torrent.New(mi.HashInfoBytes())
	require.NoError(t, err)
	tt, _, _ := cl.Start(ts)
	cl.Stop(ts)
	assert.EqualValues(t, 0, len(cl.Torrents()))
	select {
	case <-tt.GotInfo():
		t.FailNow()
	default:
	}
}

func writeTorrentData(ts storage.TorrentImpl, info metainfo.Info, b []byte) {
	for i := range iter.N(info.NumPieces()) {
		p := info.Piece(i)
		_ = errorsx.Must(ts.WriteAt(b[p.Offset():p.Offset()+p.Length()], 0))
	}
}

func testAddTorrentPriorPieceCompletion(t *testing.T, alreadyCompleted bool, csf func(*filecache.Cache) storage.ClientImpl) {
	t.SkipNow()

	// fileCacheDir, err := os.MkdirTemp("", "")
	// require.NoError(t, err)
	// defer os.RemoveAll(fileCacheDir)
	// fileCache, err := filecache.NewCache(fileCacheDir)
	// require.NoError(t, err)
	// greetingDataTempDir, greetingMetainfo := testutil.GreetingTestTorrent(t)
	// defer os.RemoveAll(greetingDataTempDir)
	// filePieceStore := csf(fileCache)
	// defer filePieceStore.Close()
	// info, err := greetingMetainfo.UnmarshalInfo()
	// require.NoError(t, err)
	// ih := greetingMetainfo.HashInfoBytes()
	// greetingData, err := storage.NewClient(filePieceStore).OpenTorrent(&info, ih)
	// require.NoError(t, err)
	// writeTorrentData(greetingData, info, []byte(testutil.GreetingFileContents))
	// for i := 0; i < info.NumPieces(); i++ {
	// 	p := info.Piece(i)
	// 	if alreadyCompleted {
	// 		require.NoError(t, greetingData.Piece(p).MarkComplete())
	// 	}
	// }
	// cfg := torrent.TestingConfig(t)
	// cl, err := autobind.NewLoopback(
	// 	autobind.DisableTCP,
	// 	autobind.DisableUTP,
	// ).Bind(torrent.NewClient(cfg))
	// require.NoError(t, err)
	// defer cl.Close()
	// ts, err := torrent.NewFromMetaInfo(greetingMetainfo, torrent.OptionStorage(filePieceStore))
	// require.NoError(t, err)
	// tt, _, err := cl.Start(ts)
	// require.NoError(t, err)
	// psrs := tt.PieceStateRuns()
	// assert.Len(t, psrs, 1)
	// assert.EqualValues(t, 3, psrs[0].Length)
	// assert.Equal(t, alreadyCompleted, psrs[0].Complete)
	// if alreadyCompleted {
	// 	r := tt.NewReader()
	// 	b, err := io.ReadAll(r)
	// 	assert.NoError(t, err)
	// 	assert.EqualValues(t, testutil.GreetingFileContents, b)
	// }
}

func TestAddTorrentPiecesAlreadyCompleted(t *testing.T) {
	testAddTorrentPriorPieceCompletion(t, true, fileCachePieceResourceStorage)
}

func TestAddTorrentPiecesNotAlreadyCompleted(t *testing.T) {
	testAddTorrentPriorPieceCompletion(t, false, fileCachePieceResourceStorage)
}

func TestTorrentDownloadAll(t *testing.T) {
	torrent.DownloadCancelTest(t, autobind.NewLoopback(), torrent.TestDownloadCancelParams{})
}

func TestTorrentDownloadAllThenCancel(t *testing.T) {
	torrent.DownloadCancelTest(t, autobind.NewLoopback(), torrent.TestDownloadCancelParams{
		Cancel: true,
	})
}

func TestPieceCompletedInStorageButNotClient(t *testing.T) {
	greetingTempDir, greetingMetainfo := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(greetingTempDir)
	cfg := torrent.TestingConfig(t)
	cfg.DataDir = greetingTempDir
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.NoError(t, err)
	ts, err := torrent.NewFromMetaInfo(greetingMetainfo, torrent.OptionStorage(storage.NewFile(greetingTempDir)))
	require.NoError(t, err)
	seeder.Start(ts)
}

func TestClientDynamicListenTCPOnly(t *testing.T) {
	cfg := torrent.TestingConfig(t)
	cl, err := autobind.NewLoopback(
		autobind.DisableUTP,
	).Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()
	assert.NotEqual(t, 0, cl.LocalPort())
}

func TestClientDynamicListenUTPOnly(t *testing.T) {
	cfg := torrent.TestingConfig(t)
	cl, err := autobind.NewLoopback(
		autobind.DisableTCP,
	).Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()
	assert.NotEqual(t, 0, cl.LocalPort())
}

// Creates a file containing its own name as data. Make a metainfo from that, adds it to the given
// client, and returns a magnet link.
func makeMagnet(t *testing.T, cl *torrent.Client, dir string, name string, store storage.ClientImpl) string {
	ctx, done := testx.Context(t)
	defer done()

	os.MkdirAll(dir, 0770)
	file, err := os.Create(filepath.Join(dir, name))
	require.NoError(t, err)
	file.Write([]byte(name))
	file.Close()
	mi := metainfo.MetaInfo{}
	mi.SetDefaults()
	info, err := metainfo.NewFromPath(
		filepath.Join(dir, name),
		metainfo.OptionPieceLength(256*1024),
	)
	require.NoError(t, err)
	mi.InfoBytes, err = bencode.Marshal(info)
	require.NoError(t, err)
	magnet := mi.Magnet(name, mi.HashInfoBytes()).String()
	ts, err := torrent.NewFromMetaInfo(&mi, torrent.OptionStorage(store))
	require.NoError(t, err)
	tr, _, err := cl.Start(ts)
	require.NoError(t, err)
	require.True(t, tr.Stats().Seeding)
	require.NoError(t, torrent.Verify(ctx, tr))
	return magnet
}

// https://github.com/anacrolix/torrent/issues/114
func TestMultipleTorrentsWithEncryption(t *testing.T) {
	testSeederLeecherPair(
		t,
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = true
			cfg.HeaderObfuscationPolicy.RequirePreferred = true
		},
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = true
			cfg.HeaderObfuscationPolicy.RequirePreferred = false
		},
	)
}

// Test that the leecher can download a torrent in its entirety from the seeder. Note that the
// seeder config is done first.
func testSeederLeecherPair(t *testing.T, seeder func(*torrent.ClientConfig), leecher func(*torrent.ClientConfig)) {
	ctx, done := testx.Context(t)
	defer done()

	cfg := torrent.TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = filepath.Join(cfg.DataDir, "server")
	cfg.Handshaker = connections.NewHandshaker(
		connections.NewFirewall(),
	)
	os.Mkdir(cfg.DataDir, 0755)
	seeder(cfg)
	server, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer server.Close()
	defer testutil.ExportStatusWriter(server, "s")()
	store := storage.NewFile(cfg.DataDir)
	magnet1 := makeMagnet(t, server, cfg.DataDir, "test1", store)
	// Extra torrents are added to test the seeder having to match incoming obfuscated headers
	// against more than one torrent. See issue #114
	makeMagnet(t, server, cfg.DataDir, "test2", store)
	for i := 0; i < 100; i++ {
		makeMagnet(t, server, cfg.DataDir, fmt.Sprintf("test%d", i+2), store)
	}
	cfg = torrent.TestingConfig(t)
	cfg.DataDir = filepath.Join(cfg.DataDir, "client")
	cfg.Handshaker = connections.NewHandshaker(
		connections.NewFirewall(),
	)
	leecher(cfg)
	client, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer client.Close()
	defer testutil.ExportStatusWriter(client, "c")()

	ts, err := torrent.NewFromMagnet(magnet1, torrent.OptionStorage(storage.NewFile(cfg.DataDir)))
	require.NoError(t, err)
	tr, _, err := client.Start(ts)
	require.NoError(t, err)

	tr.Tune(torrent.TuneClientPeer(server))

	_, err = torrent.DownloadInto(ctx, io.Discard, tr)
	require.NoError(t, err)
	client.WaitAll()
}

// This appears to be the situation with the S3 BitTorrent client.
func TestObfuscatedHeaderFallbackSeederDisallowsLeecherPrefers(t *testing.T) {
	// Leecher prefers obfuscation, but the seeder does not allow it.
	testSeederLeecherPair(
		t,
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = false
			cfg.HeaderObfuscationPolicy.RequirePreferred = true
		},
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = true
			cfg.HeaderObfuscationPolicy.RequirePreferred = false
		},
	)
}

func TestObfuscatedHeaderFallbackSeederRequiresLeecherPrefersNot(t *testing.T) {
	// Leecher prefers no obfuscation, but the seeder enforces it.
	testSeederLeecherPair(
		t,
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = true
			cfg.HeaderObfuscationPolicy.RequirePreferred = true
		},
		func(cfg *torrent.ClientConfig) {
			cfg.HeaderObfuscationPolicy.Preferred = false
			cfg.HeaderObfuscationPolicy.RequirePreferred = false
		},
	)
}

func TestRandomSizedTorrents(t *testing.T) {
	n := rand.Int63n(128 * bytesx.KiB)
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(TestingSeedConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer seeder.Close()

	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(TestingLeechConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer leecher.Close()

	testTransferRandomData(t, n, seeder, leecher)
}

func Test128KBTorrent(t *testing.T) {
	seeder, err := autobind.NewLoopback().Bind(torrent.NewClient(TestingSeedConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer seeder.Close()

	leecher, err := autobind.NewLoopback().Bind(torrent.NewClient(TestingLeechConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer leecher.Close()

	testTransferRandomData(t, 128*bytesx.KiB, seeder, leecher)
}

func testTransferRandomData(t *testing.T, n int64, from, to *torrent.Client) {
	ctx, done := testx.Context(t)
	defer done()

	data, err := testutil.IOTorrent(from.Config().DataDir, cryptox.NewChaCha8(""), n)
	require.NoError(t, err)
	defer os.Remove(data.Name())

	metadata, err := torrent.NewFromFile(data.Name(), torrent.OptionStorage(storage.NewFile(from.Config().DataDir)))
	require.NoError(t, err)

	dl, added, err := from.Start(metadata)
	require.NoError(t, err)
	require.True(t, added)

	digestseed := md5.New()
	_, err = torrent.DownloadInto(ctx, digestseed, dl)
	require.NoError(t, err)

	metadata, err = torrent.NewFromFile(data.Name(), torrent.OptionStorage(storage.NewFile(to.Config().DataDir)))
	require.NoError(t, err)

	digestdl := md5.New()
	dl, added, err = to.Start(metadata)
	require.NoError(t, err)
	require.True(t, added)

	dln, err := torrent.DownloadInto(ctx, digestdl, dl, torrent.TuneClientPeer(from))
	require.NoError(t, err)

	require.Equal(t, n, dln)
	require.Equal(t, digestseed.Sum(nil), digestdl.Sum(nil), "digest mismatch: generated torrent length", n)
}

func TestClientAddressInUse(t *testing.T) {
	s, _ := utpx.New("udp", ":50007")
	if s != nil {
		defer s.Close()
	}
	cl, err := autobind.NewSpecified(":50007").Bind(torrent.NewClient(torrent.TestingConfig(t)))
	require.Error(t, err)
	require.Nil(t, cl)
}

func TestClientHasDhtServersWhenUTPDisabled(t *testing.T) {
	cc := torrent.TestingConfig(t)
	cl, err := autobind.NewLoopback(
		autobind.DisableUTP,
	).Bind(torrent.NewClient(cc))
	require.NoError(t, err)
	defer cl.Close()
	assert.NotEmpty(t, cl.DhtServers())
}

func TestIssue335(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent(t)
	defer os.RemoveAll(dir)
	cfg := torrent.TestingConfig(t)
	cfg.Seed = false
	cfg.DataDir = dir

	cl, err := autobind.NewLoopback().Bind(torrent.NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()
	ts, err := torrent.NewFromMetaInfo(mi, torrent.OptionStorage(storage.NewMMap(dir)))
	require.NoError(t, err)
	_, added, err := cl.Start(ts)
	require.NoError(t, err)
	assert.True(t, added)
	require.True(t, cl.WaitAll())
	require.NoError(t, cl.Stop(ts))
	_, added, err = cl.Start(ts)
	require.NoError(t, err)
	assert.True(t, added)
	require.True(t, cl.WaitAll())
}
