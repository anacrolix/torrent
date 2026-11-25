package torrent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Counts calls to OpenTorrent, so tests can show storage wasn't opened.
type countingStorage struct {
	storage.ClientImpl
	opens atomic.Int32
}

func (me *countingStorage) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	me.opens.Add(1)
	return me.ClientImpl.OpenTorrent(ctx, info, infoHash)
}

func newCountingFileStorage(t *testing.T, dir string) *countingStorage {
	fs := storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir})
	t.Cleanup(func() { fs.Close() })
	return &countingStorage{ClientImpl: fs}
}

func newGreetingSeeder(t *testing.T) (*Client, *metainfo.MetaInfo) {
	greetingDir, mi := testutil.GreetingTestTorrent()
	t.Cleanup(func() { os.RemoveAll(greetingDir) })
	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = greetingDir
	seeder, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { seeder.Close() })
	seederTorrent, _, err := seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	<-seederTorrent.Complete().On()
	return seeder, mi
}

// Storage isn't opened when the info arrives from peers, and existing files are left alone until
// OpenStorage is called. The download then works as usual.
func TestDeferStorageOpenFromMagnet(t *testing.T) {
	seeder, mi := newGreetingSeeder(t)
	leecher, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer leecher.Close()
	dataDir := t.TempDir()
	existing := filepath.Join(dataDir, testutil.GreetingFileName)
	const existingContents = "not the greeting, don't touch"
	qt.Assert(t, qt.IsNil(os.WriteFile(existing, []byte(existingContents), 0o644)))
	cs := newCountingFileStorage(t, dataDir)
	tor, _ := leecher.AddTorrentOpt(AddTorrentOpts{
		InfoHash:         mi.HashInfoBytes(),
		Storage:          cs,
		DeferStorageOpen: true,
	})
	qt.Assert(t, qt.ErrorMatches(tor.OpenStorage(context.Background(), nil), "torrent info not available"))
	tor.AddClientPeer(seeder)
	<-tor.GotInfo()
	tor.DownloadAll()
	r := tor.NewReader()
	defer r.Close()
	func() {
		leecher.lock()
		defer leecher.unlock()
		qt.Check(t, qt.IsNil(tor.storage))
		qt.Check(t, qt.Equals(tor.numPiecesCompleted(), 0))
		// Nothing is requested without storage.
		for pc := range tor.conns {
			qt.Check(t, qt.IsTrue(pc.requestState.Requests.IsEmpty()))
		}
	}()
	qt.Check(t, qt.Equals(cs.opens.Load(), 0))
	b, err := os.ReadFile(existing)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(string(b), existingContents))
	entries, err := os.ReadDir(dataDir)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.HasLen(entries, 1))

	// The caller has decided the existing file can go.
	qt.Assert(t, qt.IsNil(os.Remove(existing)))
	qt.Assert(t, qt.IsNil(tor.OpenStorage(context.Background(), nil)))
	qt.Check(t, qt.Equals(cs.opens.Load(), 1))
	qt.Check(t, qt.ErrorMatches(tor.OpenStorage(context.Background(), nil), "storage already opened"))
	assertReadAllGreeting(t, r)
	<-tor.Complete().On()
}

// Data already in storage is found once it's opened, using storage passed to OpenStorage.
func TestDeferStorageOpenFindsExistingData(t *testing.T) {
	greetingDir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(greetingDir)
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tor, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:         mi.HashInfoBytes(),
		InfoBytes:        mi.InfoBytes,
		DeferStorageOpen: true,
	})
	<-tor.GotInfo()
	qt.Check(t, qt.IsFalse(tor.Complete().Bool()))
	cs := newCountingFileStorage(t, greetingDir)
	qt.Assert(t, qt.IsNil(tor.OpenStorage(context.Background(), cs)))
	qt.Check(t, qt.Equals(cs.opens.Load(), 1))
	<-tor.Complete().On()
	r := tor.NewReader()
	defer r.Close()
	assertReadAllGreeting(t, r)
}

func TestOpenStorageErrors(t *testing.T) {
	mi := testutil.GreetingMetaInfo()
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tor, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:  mi.HashInfoBytes(),
		InfoBytes: mi.InfoBytes,
	})
	qt.Check(t, qt.ErrorMatches(
		tor.OpenStorage(context.Background(), nil),
		"torrent doesn't defer opening storage"))
	tor.Drop()

	cs := newCountingFileStorage(t, t.TempDir())
	tor, _ = cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:         mi.HashInfoBytes(),
		InfoBytes:        mi.InfoBytes,
		Storage:          cs,
		DeferStorageOpen: true,
	})
	// Dropping before storage is opened is fine.
	tor.Drop()
	qt.Check(t, qt.ErrorIs(tor.OpenStorage(context.Background(), nil), errTorrentClosed))
	qt.Check(t, qt.Equals(cs.opens.Load(), 0))
}
