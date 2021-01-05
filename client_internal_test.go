package torrent

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/anacrolix/missinggo/filecache"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/utp"
	"github.com/bradfitz/iter"
	"github.com/james-lawrence/torrent/connections"
	"github.com/james-lawrence/torrent/dht/v2"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/sockets"
	"github.com/james-lawrence/torrent/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tt interface {
	require.TestingT
	TempDir() string
}

func TestingConfig(t tt) *ClientConfig {
	cfg := NewDefaultClientConfig()
	cfg.NoDHT = true
	cfg.DataDir = testutil.Autodir(t)
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableAcceptRateLimiting = true
	// cfg.Debug = log.New(os.Stderr, "[debug] ", log.Flags())
	return cfg
}

func Autosocket(t *testing.T) Binder {
	var (
		err      error
		bindings []socket
		tsocket  *utp.Socket
	)

	tsocket, err = utp.NewSocket("udp", "localhost:0")
	require.NoError(t, err)

	bindings = append(bindings, sockets.New(tsocket, tsocket))

	if addr, ok := tsocket.Addr().(*net.UDPAddr); ok {
		s, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", addr.Port))
		require.NoError(t, err)
		bindings = append(bindings, sockets.New(s, &net.Dialer{}))
	}

	return NewSocketsBind(bindings...)
}

func totalConns(tts []Torrent) (ret int) {
	for _, tt := range tts {
		ret += tt.Stats().ActivePeers
	}
	return
}

// DeprecatedAccounceList - used to reach into a torrent for tests.
func DeprecatedAnnounceList(t Torrent) (map[string]*trackerScraper, bool) {
	if tt, ok := t.(*torrent); ok {
		return tt.trackerAnnouncers, ok
	}
	return make(map[string]*trackerScraper), false
}

// DeprecatedExtractClient - used to extract the underlying client.
func DeprecatedExtractClient(t Torrent) *Client {
	return t.(*torrent).cln
}

func TestTorrentInitialState(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	cl, err := NewClient(TestingConfig(t))
	require.NoError(t, err)

	tt, err := New(
		mi.HashInfoBytes(),
		OptionStorage(storage.NewFileWithCompletion(t.TempDir(), storage.NewMapPieceCompletion())),
		OptionChunk(2),
		OptionInfo(mi.InfoBytes),
	)
	require.NoError(t, err)
	tor := cl.newTorrent(tt)

	require.Len(t, tor.pieces, 3)
	tor.pendAllChunkSpecs(0)
	tor.lock()
	assert.EqualValues(t, 3, int(tor.pieceNumPendingChunks(0)))
	tor.unlock()
	assert.EqualValues(t, chunkSpec{4, 1}, chunkIndexSpec(2, tor.pieceLength(0), tor.chunkSize))
}

func TestReducedDialTimeout(t *testing.T) {
	cfg := NewDefaultClientConfig()
	for _, _case := range []struct {
		Max             time.Duration
		HalfOpenLimit   int
		PendingPeers    int
		ExpectedReduced time.Duration
	}{
		{cfg.NominalDialTimeout, 40, 0, cfg.NominalDialTimeout},
		{cfg.NominalDialTimeout, 40, 1, cfg.NominalDialTimeout},
		{cfg.NominalDialTimeout, 40, 39, cfg.NominalDialTimeout},
		{cfg.NominalDialTimeout, 40, 40, cfg.NominalDialTimeout / 2},
		{cfg.NominalDialTimeout, 40, 80, cfg.NominalDialTimeout / 3},
		{cfg.NominalDialTimeout, 40, 4000, cfg.NominalDialTimeout / 101},
	} {
		reduced := reducedDialTimeout(cfg.MinDialTimeout, _case.Max, _case.HalfOpenLimit, _case.PendingPeers)
		expected := _case.ExpectedReduced
		if expected < cfg.MinDialTimeout {
			expected = cfg.MinDialTimeout
		}
		if reduced != expected {
			t.Fatalf("expected %s, got %s", _case.ExpectedReduced, reduced)
		}
	}
}

type TestDownloadCancelParams struct {
	SetLeecherStorageCapacity bool
	LeecherStorageCapacity    int64
	Cancel                    bool
}

func DownloadCancelTest(t *testing.T, b Binder, ps TestDownloadCancelParams) {
	greetingTempDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingTempDir)
	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = greetingTempDir
	seeder, err := b.Bind(NewClient(cfg))
	require.NoError(t, err)
	defer seeder.Close()
	defer testutil.ExportStatusWriter(seeder, "s")()
	tt, err := NewFromMetaInfo(mi)
	require.NoError(t, err)
	seederTorrent, _, _ := seeder.Start(tt)
	seederTorrent.VerifyData()
	leecherDataDir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(leecherDataDir)
	fc, err := filecache.NewCache(leecherDataDir)
	require.NoError(t, err)
	if ps.SetLeecherStorageCapacity {
		fc.SetCapacity(ps.LeecherStorageCapacity)
	}
	cfg.DefaultStorage = storage.NewResourcePieces(fc.AsResourceProvider())
	cfg.DataDir = leecherDataDir
	leecher, err := b.Bind(NewClient(cfg))
	require.NoError(t, err)
	defer leecher.Close()
	defer testutil.ExportStatusWriter(leecher, "l")()
	t2, err := NewFromMetaInfo(mi, OptionChunk(2))
	require.NoError(t, err)
	leecherGreeting, added, err := leecher.Start(t2)
	require.NoError(t, err)
	assert.True(t, added)
	psc := leecherGreeting.SubscribePieceStateChanges()
	defer psc.Close()

	leecherGreeting.(*torrent).lock()
	leecherGreeting.(*torrent).downloadPiecesLocked(0, leecherGreeting.(*torrent).numPieces())
	if ps.Cancel {
		leecherGreeting.(*torrent).cancelPiecesLocked(0, leecherGreeting.(*torrent).NumPieces())
	}
	leecherGreeting.(*torrent).unlock()

	done := make(chan struct{})
	defer close(done)
	go leecherGreeting.Tune(TuneClientPeer(seeder))
	completes := make(map[int]bool, 3)
	expected := func() map[int]bool {
		if ps.Cancel {
			return map[int]bool{0: false, 1: false, 2: false}
		}
		return map[int]bool{0: true, 1: true, 2: true}
	}()
	for !reflect.DeepEqual(completes, expected) {
		_v := <-psc.Values
		v := _v.(PieceStateChange)
		completes[v.Index] = v.Complete
	}
}

// Ensure that it's an error for a peer to send an invalid have message.
func TestPeerInvalidHave(t *testing.T) {
	cl, err := Autosocket(t).Bind(NewClient(TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	info := metainfo.Info{
		PieceLength: 1,
		Pieces:      make([]byte, 20),
		Files:       []metainfo.FileInfo{{Length: 1}},
	}

	ts, err := NewFromInfo(info, OptionStorage(testutil.NewBadStorage()))
	require.NoError(t, err)
	tt, _added, err := cl.Start(ts)
	require.NoError(t, err)
	assert.True(t, _added)
	defer cl.Stop(ts)
	cn := newConnection(nil, true, IpPort{})
	cn.t = tt.(*torrent)
	assert.NoError(t, cn.peerSentHave(0))
	assert.Error(t, cn.peerSentHave(1))
}

// Check that when the listen port is 0, all the protocols listened on have
// the same port, and it isn't zero.
func TestClientDynamicListenPortAllProtocols(t *testing.T) {
	cl, err := Autosocket(t).Bind(NewClient(TestingConfig(t)))
	require.NoError(t, err)
	defer cl.Close()
	port := cl.LocalPort()
	assert.NotEqual(t, 0, port)
	cl.eachListener(func(s socket) bool {
		assert.Equal(t, port, missinggo.AddrPort(s.Addr()))
		return true
	})
}

func TestSetMaxEstablishedConn(t *testing.T) {
	var tts []Torrent
	mi := testutil.GreetingMetaInfo().HashInfoBytes()
	for i := range iter.N(3) {
		cfg := TestingConfig(t)
		cfg.DisableAcceptRateLimiting = true
		cfg.dropDuplicatePeerIds = true
		cfg.Handshaker = connections.NewHandshaker(
			connections.NewFirewall(),
		)
		cl, err := Autosocket(t).Bind(NewClient(cfg))
		require.NoError(t, err)
		defer cl.Close()
		ts, err := New(mi)
		require.NoError(t, err)
		tt, _, _ := cl.Start(ts)
		require.NoError(t, tt.Tune(TuneMaxConnections(2)))
		defer testutil.ExportStatusWriter(cl, fmt.Sprintf("%d", i))()
		tts = append(tts, tt)
	}
	addPeers := func() {
		for _, tt := range tts {
			for _, _tt := range tts {
				tt.Tune(TuneClientPeer(DeprecatedExtractClient(_tt)))
			}
		}
	}
	waitTotalConns := func(num int) {
		for tot := totalConns(tts); tot != num; tot = totalConns(tts) {
			addPeers()
			time.Sleep(time.Millisecond)
		}
	}

	addPeers()
	waitTotalConns(6)
	tts[0].Tune(TuneMaxConnections(1))
	waitTotalConns(4)
	tts[0].Tune(TuneMaxConnections(0))
	waitTotalConns(2)
	tts[0].Tune(TuneMaxConnections(1))
	addPeers()
	waitTotalConns(4)
	tts[0].Tune(TuneMaxConnections(2))
	addPeers()
	waitTotalConns(6)
}

func TestAddMetainfoWithNodes(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.NoDHT = false
	cfg.DhtStartingNodes = func() ([]dht.Addr, error) { return nil, nil }
	// For now, we want to just jam the nodes into the table, without
	// verifying them first. Also the DHT code doesn't support mixing secure
	// and insecure nodes if security is enabled (yet).
	// cfg.DHTConfig.NoSecurity = true
	cl, err := Autosocket(t).Bind(NewClient(cfg))
	require.NoError(t, err)
	defer cl.Close()
	sum := func() (ret int64) {
		cl.eachDhtServer(func(s *dht.Server) {
			ret += s.Stats().OutboundQueriesAttempted
		})
		return
	}
	assert.EqualValues(t, 0, sum())
	ts, err := NewFromMetaInfoFile("metainfo/testdata/issue_65a.torrent")
	require.NoError(t, err)
	tt, _, err := cl.Start(ts)
	require.NoError(t, err)
	// Nodes are not added or exposed in Torrent's metainfo. We just randomly
	// check if the announce-list is here instead. TODO: Add nodes.
	assert.Len(t, tt.Metadata().Trackers, 5)
	// There are 6 nodes in the torrent file.
	for sum() != int64(6*len(cl.dhtServers)) {
		time.Sleep(time.Millisecond)
	}
}
