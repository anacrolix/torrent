package torrent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/anacrolix/envpprof"
	"github.com/anacrolix/missinggo"
	. "github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/filecache"
	"github.com/anacrolix/utp"
	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/data/pieceStore"
	"github.com/anacrolix/torrent/data/pieceStore/dataBackend/fileCache"
	"github.com/anacrolix/torrent/dht"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/iplist"
	"github.com/anacrolix/torrent/metainfo"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Llongfile)
}

var TestingConfig = Config{
	ListenAddr:           "localhost:0",
	NoDHT:                true,
	DisableTrackers:      true,
	NoDefaultBlocklist:   true,
	DisableMetainfoCache: true,
	DataDir:              filepath.Join(os.TempDir(), "anacrolix"),
	DHTConfig: dht.ServerConfig{
		NoDefaultBootstrap: true,
	},
}

func TestClientDefault(t *testing.T) {
	cl, err := NewClient(&TestingConfig)
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
}

func TestAddDropTorrent(t *testing.T) {
	cl, err := NewClient(&TestingConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tt, new, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	if err != nil {
		t.Fatal(err)
	}
	if !new {
		t.FailNow()
	}
	tt.Drop()
}

func TestAddTorrentNoSupportedTrackerSchemes(t *testing.T) {
	t.SkipNow()
}

func TestAddTorrentNoUsableURLs(t *testing.T) {
	t.SkipNow()
}

func TestAddPeersToUnknownTorrent(t *testing.T) {
	t.SkipNow()
}

func TestPieceHashSize(t *testing.T) {
	if pieceHash.Size() != 20 {
		t.FailNow()
	}
}

func TestTorrentInitialState(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tor := newTorrent(func() (ih InfoHash) {
		missinggo.CopyExact(ih[:], mi.Info.Hash)
		return
	}())
	tor.chunkSize = 2
	err := tor.setMetadata(&mi.Info.Info, mi.Info.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(tor.Pieces) != 3 {
		t.Fatal("wrong number of pieces")
	}
	tor.pendAllChunkSpecs(0)
	assert.EqualValues(t, 3, tor.pieceNumPendingChunks(0))
	assert.EqualValues(t, chunkSpec{4, 1}, chunkIndexSpec(2, tor.pieceLength(0), tor.chunkSize))
}

func TestUnmarshalPEXMsg(t *testing.T) {
	var m peerExchangeMessage
	if err := bencode.Unmarshal([]byte("d5:added12:\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0ce"), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Added) != 2 {
		t.FailNow()
	}
	if m.Added[0].Port != 0x506 {
		t.FailNow()
	}
}

func TestReducedDialTimeout(t *testing.T) {
	for _, _case := range []struct {
		Max             time.Duration
		HalfOpenLimit   int
		PendingPeers    int
		ExpectedReduced time.Duration
	}{
		{nominalDialTimeout, 40, 0, nominalDialTimeout},
		{nominalDialTimeout, 40, 1, nominalDialTimeout},
		{nominalDialTimeout, 40, 39, nominalDialTimeout},
		{nominalDialTimeout, 40, 40, nominalDialTimeout / 2},
		{nominalDialTimeout, 40, 80, nominalDialTimeout / 3},
		{nominalDialTimeout, 40, 4000, nominalDialTimeout / 101},
	} {
		reduced := reducedDialTimeout(_case.Max, _case.HalfOpenLimit, _case.PendingPeers)
		expected := _case.ExpectedReduced
		if expected < minDialTimeout {
			expected = minDialTimeout
		}
		if reduced != expected {
			t.Fatalf("expected %s, got %s", _case.ExpectedReduced, reduced)
		}
	}
}

func TestUTPRawConn(t *testing.T) {
	l, err := utp.NewSocket("udp", "")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			_, err := l.Accept()
			if err != nil {
				break
			}
		}
	}()
	// Connect a UTP peer to see if the RawConn will still work.
	s, _ := utp.NewSocket("udp", "")
	defer s.Close()
	utpPeer, err := s.Dial(fmt.Sprintf("localhost:%d", missinggo.AddrPort(l.Addr())))
	if err != nil {
		t.Fatalf("error dialing utp listener: %s", err)
	}
	defer utpPeer.Close()
	peer, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	msgsReceived := 0
	// How many messages to send. I've set this to double the channel buffer
	// size in the raw packetConn.
	const N = 200
	readerStopped := make(chan struct{})
	// The reader goroutine.
	go func() {
		defer close(readerStopped)
		b := make([]byte, 500)
		for i := 0; i < N; i++ {
			n, _, err := l.ReadFrom(b)
			if err != nil {
				t.Fatalf("error reading from raw conn: %s", err)
			}
			msgsReceived++
			var d int
			fmt.Sscan(string(b[:n]), &d)
			if d != i {
				log.Printf("got wrong number: expected %d, got %d", i, d)
			}
		}
	}()
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("localhost:%d", missinggo.AddrPort(l.Addr())))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < N; i++ {
		_, err := peer.WriteTo([]byte(fmt.Sprintf("%d", i)), udpAddr)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Microsecond)
	}
	select {
	case <-readerStopped:
	case <-time.After(time.Second):
		t.Fatal("reader timed out")
	}
	if msgsReceived != N {
		t.Fatalf("messages received: %d", msgsReceived)
	}
}

func TestTwoClientsArbitraryPorts(t *testing.T) {
	for i := 0; i < 2; i++ {
		cl, err := NewClient(&TestingConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
	}
}

func TestAddDropManyTorrents(t *testing.T) {
	cl, _ := NewClient(&TestingConfig)
	defer cl.Close()
	for i := range iter.N(1000) {
		var spec TorrentSpec
		binary.PutVarint(spec.InfoHash[:], int64(i))
		tt, new, err := cl.AddTorrentSpec(&spec)
		if err != nil {
			t.Error(err)
		}
		if !new {
			t.FailNow()
		}
		defer tt.Drop()
	}
}

func TestClientTransferDefault(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{})
}

func TestClientTransferSmallCache(t *testing.T) {
	testClientTransfer(t, testClientTransferParams{
		SetLeecherStorageCapacity: true,
		// Going below the piece length means it can't complete a piece so
		// that it can be hashed.
		LeecherStorageCapacity: 5,
		SetReadahead:           true,
		// Can't readahead too far or the cache will thrash and drop data we
		// thought we had.
		Readahead:          0,
		ExportClientStatus: true,
	})
}

func TestClientTransferVarious(t *testing.T) {
	for _, responsive := range []bool{false, true} {
		testClientTransfer(t, testClientTransferParams{
			Responsive: responsive,
		})
		for _, readahead := range []int64{-1, 0, 1, 2, 3, 4, 5, 6, 9, 10, 11, 12, 13, 14, 15, 20} {
			testClientTransfer(t, testClientTransferParams{
				Responsive:   responsive,
				SetReadahead: true,
				Readahead:    readahead,
			})
		}
	}
}

type testClientTransferParams struct {
	Responsive                bool
	Readahead                 int64
	SetReadahead              bool
	ExportClientStatus        bool
	SetLeecherStorageCapacity bool
	LeecherStorageCapacity    int64
}

func testClientTransfer(t *testing.T, ps testClientTransferParams) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	cfg := TestingConfig
	cfg.Seed = true
	cfg.DataDir = greetingTempDir
	seeder, err := NewClient(&cfg)
	require.NoError(t, err)
	defer seeder.Close()
	if ps.ExportClientStatus {
		testutil.ExportStatusWriter(seeder, "s")
	}
	seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherDataDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg.TorrentDataOpener = func() TorrentDataOpener {
		fc, err := filecache.NewCache(leecherDataDir)
		require.NoError(t, err)
		if ps.SetLeecherStorageCapacity {
			fc.SetCapacity(ps.LeecherStorageCapacity)
		}
		store := pieceStore.New(fileCacheDataBackend.New(fc))
		return func(mi *metainfo.Info) Data {
			return store.OpenTorrentData(mi)
		}
	}()
	leecher, _ := NewClient(&cfg)
	defer leecher.Close()
	if ps.ExportClientStatus {
		testutil.ExportStatusWriter(leecher, "l")
	}
	leecherGreeting, new, err := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	require.NoError(t, err)
	assert.True(t, new)
	leecherGreeting.AddPeers([]Peer{
		Peer{
			IP:   missinggo.AddrIP(seeder.ListenAddr()),
			Port: missinggo.AddrPort(seeder.ListenAddr()),
		},
	})
	r := leecherGreeting.NewReader()
	defer r.Close()
	if ps.Responsive {
		r.SetResponsive()
	}
	if ps.SetReadahead {
		r.SetReadahead(ps.Readahead)
	}
	for range iter.N(2) {
		pos, err := r.Seek(0, os.SEEK_SET)
		assert.NoError(t, err)
		assert.EqualValues(t, 0, pos)
		_greeting, err := ioutil.ReadAll(r)
		assert.NoError(t, err)
		assert.EqualValues(t, testutil.GreetingFileContents, _greeting)
	}
}

// Check that after completing leeching, a leecher transitions to a seeding
// correctly. Connected in a chain like so: Seeder <-> Leecher <-> LeecherLeecher.
func TestSeedAfterDownloading(t *testing.T) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	cfg := TestingConfig
	cfg.Seed = true
	cfg.DataDir = greetingTempDir
	seeder, err := NewClient(&cfg)
	defer seeder.Close()
	testutil.ExportStatusWriter(seeder, "s")
	seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	cfg.DataDir, err = ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecher, _ := NewClient(&cfg)
	defer leecher.Close()
	testutil.ExportStatusWriter(leecher, "l")
	cfg.Seed = false
	cfg.TorrentDataOpener = nil
	cfg.DataDir, err = ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(cfg.DataDir)
	leecherLeecher, _ := NewClient(&cfg)
	defer leecherLeecher.Close()
	testutil.ExportStatusWriter(leecherLeecher, "ll")
	leecherGreeting, _, _ := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	llg, _, _ := leecherLeecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 3
		return
	}())
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
	leecherGreeting.AddPeers([]Peer{
		Peer{
			IP:   missinggo.AddrIP(seeder.ListenAddr()),
			Port: missinggo.AddrPort(seeder.ListenAddr()),
		},
		Peer{
			IP:   missinggo.AddrIP(leecherLeecher.ListenAddr()),
			Port: missinggo.AddrPort(leecherLeecher.ListenAddr()),
		},
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		leecherGreeting.DownloadAll()
		leecher.WaitAll()
	}()
	wg.Wait()
}

func TestReadaheadPieces(t *testing.T) {
	for _, case_ := range []struct {
		readaheadBytes, pieceLength int64
		readaheadPieces             int
	}{
		{5 * 1024 * 1024, 256 * 1024, 19},
		{5 * 1024 * 1024, 5 * 1024 * 1024, 1},
		{5*1024*1024 - 1, 5 * 1024 * 1024, 1},
		{5 * 1024 * 1024, 5*1024*1024 - 1, 2},
		{0, 5 * 1024 * 1024, 0},
		{5 * 1024 * 1024, 1048576, 4},
	} {
		pieces := readaheadPieces(case_.readaheadBytes, case_.pieceLength)
		assert.Equal(t, case_.readaheadPieces, pieces, "%v", case_)
	}
}

func TestMergingTrackersByAddingSpecs(t *testing.T) {
	cl, err := NewClient(&TestingConfig)
	require.NoError(t, err)
	defer cl.Close()
	spec := TorrentSpec{}
	T, new, _ := cl.AddTorrentSpec(&spec)
	if !new {
		t.FailNow()
	}
	spec.Trackers = [][]string{{"http://a"}, {"udp://b"}}
	_, new, _ = cl.AddTorrentSpec(&spec)
	if new {
		t.FailNow()
	}
	assert.EqualValues(t, T.torrent.Trackers[0][0], "http://a")
	assert.EqualValues(t, T.torrent.Trackers[1][0], "udp://b")
}

type badData struct{}

func (me badData) Close() {}

func (me badData) WriteAt(b []byte, off int64) (int, error) {
	return 0, nil
}

func (me badData) PieceComplete(piece int) bool {
	return true
}

func (me badData) PieceCompleted(piece int) error {
	return errors.New("psyyyyyyyche")
}

func (me badData) randomlyTruncatedDataString() string {
	return "hello, world\n"[:rand.Intn(14)]
}

func (me badData) ReadAt(b []byte, off int64) (n int, err error) {
	r := strings.NewReader(me.randomlyTruncatedDataString())
	return r.ReadAt(b, off)
}

// We read from a piece which is marked completed, but is missing data.
func TestCompletedPieceWrongSize(t *testing.T) {
	cfg := TestingConfig
	cfg.TorrentDataOpener = func(*metainfo.Info) Data {
		return badData{}
	}
	cl, _ := NewClient(&cfg)
	defer cl.Close()
	tt, new, err := cl.AddTorrentSpec(&TorrentSpec{
		Info: &metainfo.InfoEx{
			Info: metainfo.Info{
				PieceLength: 15,
				Pieces:      make([]byte, 20),
				Files: []metainfo.FileInfo{
					metainfo.FileInfo{Path: []string{"greeting"}, Length: 13},
				},
			},
		},
	})
	require.NoError(t, err)
	defer tt.Drop()
	assert.True(t, new)
	r := tt.NewReader()
	defer r.Close()
	b, err := ioutil.ReadAll(r)
	assert.Len(t, b, 13)
	assert.NoError(t, err)
}

func BenchmarkAddLargeTorrent(b *testing.B) {
	cfg := TestingConfig
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.ListenAddr = "redonk"
	cl, _ := NewClient(&cfg)
	defer cl.Close()
	for range iter.N(b.N) {
		t, err := cl.AddTorrentFromFile("testdata/bootstrap.dat.torrent")
		if err != nil {
			b.Fatal(err)
		}
		t.Drop()
	}
}

func TestResponsive(t *testing.T) {
	seederDataDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(seederDataDir)
	cfg := TestingConfig
	cfg.Seed = true
	cfg.DataDir = seederDataDir
	seeder, err := NewClient(&cfg)
	require.Nil(t, err)
	defer seeder.Close()
	seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherDataDir, err := ioutil.TempDir("", "")
	require.Nil(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg = TestingConfig
	cfg.DataDir = leecherDataDir
	leecher, err := NewClient(&cfg)
	require.Nil(t, err)
	defer leecher.Close()
	leecherTorrent, _, _ := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	leecherTorrent.AddPeers([]Peer{
		Peer{
			IP:   missinggo.AddrIP(seeder.ListenAddr()),
			Port: missinggo.AddrPort(seeder.ListenAddr()),
		},
	})
	reader := leecherTorrent.NewReader()
	defer reader.Close()
	reader.SetReadahead(0)
	reader.SetResponsive()
	b := make([]byte, 2)
	_, err = reader.Seek(3, os.SEEK_SET)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, "lo", string(b))
	_, err = reader.Seek(11, os.SEEK_SET)
	require.NoError(t, err)
	n, err := io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, "d\n", string(b))
}

func TestTorrentDroppedDuringResponsiveRead(t *testing.T) {
	seederDataDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(seederDataDir)
	cfg := TestingConfig
	cfg.Seed = true
	cfg.DataDir = seederDataDir
	seeder, err := NewClient(&cfg)
	require.Nil(t, err)
	defer seeder.Close()
	seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	leecherDataDir, err := ioutil.TempDir("", "")
	require.Nil(t, err)
	defer os.RemoveAll(leecherDataDir)
	cfg = TestingConfig
	cfg.DataDir = leecherDataDir
	leecher, err := NewClient(&cfg)
	require.Nil(t, err)
	defer leecher.Close()
	leecherTorrent, _, _ := leecher.AddTorrentSpec(func() (ret *TorrentSpec) {
		ret = TorrentSpecFromMetaInfo(mi)
		ret.ChunkSize = 2
		return
	}())
	leecherTorrent.AddPeers([]Peer{
		Peer{
			IP:   missinggo.AddrIP(seeder.ListenAddr()),
			Port: missinggo.AddrPort(seeder.ListenAddr()),
		},
	})
	reader := leecherTorrent.NewReader()
	defer reader.Close()
	reader.SetReadahead(0)
	reader.SetResponsive()
	b := make([]byte, 2)
	_, err = reader.Seek(3, os.SEEK_SET)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, b)
	assert.Nil(t, err)
	assert.EqualValues(t, "lo", string(b))
	go leecherTorrent.Drop()
	_, err = reader.Seek(11, os.SEEK_SET)
	require.NoError(t, err)
	n, err := reader.Read(b)
	assert.EqualError(t, err, "torrent closed")
	assert.EqualValues(t, 0, n)
}

func TestDHTInheritBlocklist(t *testing.T) {
	ipl := iplist.New(nil)
	require.NotNil(t, ipl)
	cfg := TestingConfig
	cfg.IPBlocklist = ipl
	cfg.NoDHT = false
	cl, err := NewClient(&cfg)
	require.NoError(t, err)
	defer cl.Close()
	require.Equal(t, ipl, cl.DHT().IPBlocklist())
}

// Check that stuff is merged in subsequent AddTorrentSpec for the same
// infohash.
func TestAddTorrentSpecMerging(t *testing.T) {
	cl, err := NewClient(&TestingConfig)
	require.NoError(t, err)
	defer cl.Close()
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	var ts TorrentSpec
	missinggo.CopyExact(&ts.InfoHash, mi.Info.Hash)
	tt, new, err := cl.AddTorrentSpec(&ts)
	require.NoError(t, err)
	require.True(t, new)
	require.Nil(t, tt.Info())
	_, new, err = cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	require.False(t, new)
	require.NotNil(t, tt.Info())
}

// Check that torrent Info is obtained from the metainfo file cache.
func TestAddTorrentMetainfoInCache(t *testing.T) {
	cfg := TestingConfig
	cfg.DisableMetainfoCache = false
	cfg.ConfigDir, _ = ioutil.TempDir(os.TempDir(), "")
	defer os.RemoveAll(cfg.ConfigDir)
	cl, err := NewClient(&cfg)
	require.NoError(t, err)
	defer cl.Close()
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tt, new, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	require.NoError(t, err)
	require.True(t, new)
	require.NotNil(t, tt.Info())
	_, err = os.Stat(filepath.Join(cfg.ConfigDir, "torrents", fmt.Sprintf("%x.torrent", mi.Info.Hash)))
	require.NoError(t, err)
	// Contains only the infohash.
	var ts TorrentSpec
	missinggo.CopyExact(&ts.InfoHash, mi.Info.Hash)
	_, ok := cl.Torrent(ts.InfoHash)
	require.True(t, ok)
	tt.Drop()
	_, ok = cl.Torrent(ts.InfoHash)
	require.False(t, ok)
	tt, new, err = cl.AddTorrentSpec(&ts)
	require.NoError(t, err)
	require.True(t, new)
	// Obtained from the metainfo cache.
	require.NotNil(t, tt.Info())
}

func TestTorrentDroppedBeforeGotInfo(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	os.RemoveAll(dir)
	cl, _ := NewClient(&TestingConfig)
	defer cl.Close()
	var ts TorrentSpec
	CopyExact(&ts.InfoHash, mi.Info.Hash)
	tt, _, _ := cl.AddTorrentSpec(&ts)
	tt.Drop()
	assert.EqualValues(t, 0, len(cl.Torrents()))
	select {
	case <-tt.GotInfo():
		t.FailNow()
	default:
	}
}

func testAddTorrentPriorPieceCompletion(t *testing.T, alreadyCompleted bool) {
	fileCacheDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(fileCacheDir)
	fileCache, err := filecache.NewCache(fileCacheDir)
	require.NoError(t, err)
	greetingDataTempDir, greetingMetainfo := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingDataTempDir)
	filePieceStore := pieceStore.New(fileCacheDataBackend.New(fileCache))
	greetingData := filePieceStore.OpenTorrentData(&greetingMetainfo.Info.Info)
	written, err := greetingData.WriteAt([]byte(testutil.GreetingFileContents), 0)
	require.Equal(t, len(testutil.GreetingFileContents), written)
	require.NoError(t, err)
	for i := 0; i < greetingMetainfo.Info.NumPieces(); i++ {
		// p := greetingMetainfo.Info.Piece(i)
		if alreadyCompleted {
			err := greetingData.PieceCompleted(i)
			assert.NoError(t, err)
		}
	}
	cfg := TestingConfig
	// TODO: Disable network option?
	cfg.DisableTCP = true
	cfg.DisableUTP = true
	cfg.TorrentDataOpener = func(mi *metainfo.Info) Data {
		return filePieceStore.OpenTorrentData(mi)
	}
	cl, err := NewClient(&cfg)
	require.NoError(t, err)
	defer cl.Close()
	tt, err := cl.AddTorrent(greetingMetainfo)
	require.NoError(t, err)
	psrs := tt.PieceStateRuns()
	assert.Len(t, psrs, 1)
	assert.EqualValues(t, 3, psrs[0].Length)
	assert.Equal(t, alreadyCompleted, psrs[0].Complete)
	if alreadyCompleted {
		r := tt.NewReader()
		b, err := ioutil.ReadAll(r)
		assert.NoError(t, err)
		assert.EqualValues(t, testutil.GreetingFileContents, b)
	}
}

func TestAddTorrentPiecesAlreadyCompleted(t *testing.T) {
	testAddTorrentPriorPieceCompletion(t, true)
}

func TestAddTorrentPiecesNotAlreadyCompleted(t *testing.T) {
	testAddTorrentPriorPieceCompletion(t, false)
}
