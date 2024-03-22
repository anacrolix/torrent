package webseed_partial_seed

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/internal/testutil"
	qt "github.com/frankban/quicktest"
)

func testdataDir() string {
	_, b, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(b), "../../testdata")
}

func makeSeederClient(t *testing.T) *torrent.Client {
	config := torrent.TestingConfig(t)
	config.Seed = true
	config.MaxAllocPeerRequestDataPerConn = 1 << 20
	c, err := torrent.NewClient(config)
	assertOk(err)
	return c
}

func makeLeecherClient(t *testing.T) *torrent.Client {
	config := torrent.TestingConfig(t)
	config.Debug = true
	config.Logger = config.Logger.WithNames("leecher")
	config.DisableWebseeds = true
	c, err := torrent.NewClient(config)
	assertOk(err)
	return c
}

func assertOk(err error) {
	if err != nil {
		panic(err)
	}
}

func downloadAll(t *torrent.Torrent) {
	<-t.GotInfo()
	t.DownloadAll()
}

func TestWebseedPartialSeed(t *testing.T) {
	c := qt.New(t)
	seederClient := makeSeederClient(t)
	defer seederClient.Close()
	testutil.ExportStatusWriter(seederClient, "seeder", t)
	const infoHashHex = "a88fda5954e89178c372716a6a78b8180ed4dad3"
	metainfoPath := filepath.Join(testdataDir(), "The WIRED CD - Rip. Sample. Mash. Share.torrent")
	seederTorrent, err := seederClient.AddTorrentFromFile(metainfoPath)
	assertOk(err)
	leecherClient := makeLeecherClient(t)
	defer leecherClient.Close()
	testutil.ExportStatusWriter(leecherClient, "leecher", t)
	leecherTorrent, _ := leecherClient.AddTorrentFromFile(metainfoPath)
	// TODO: Check that leecher has pieces before seeder completes. Currently I do this manually by
	// looking at the GOPPROF http endpoint with the exported status writer
	// /TestWebseedPartialSeed/leecher.
	go downloadAll(leecherTorrent)
	go leecherTorrent.AddClientPeer(seederClient)
	seederTorrent.DownloadAll()
	allDownloaded := leecherClient.WaitAll()
	c.Assert(allDownloaded, qt.IsTrue)
}
