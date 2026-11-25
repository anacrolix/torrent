package torrent

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func makeWebseedTestMetaInfo(t *testing.T, srvDir, srvURL, name string, size int) *metainfo.MetaInfo {
	t.Helper()
	root := filepath.Join(srvDir, name)
	qt.Assert(t, qt.IsNil(os.MkdirAll(root, 0o755)))
	data := make([]byte, size)
	_, err := rand.Read(data)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "file.bin"), data, 0o644)))
	info := metainfo.Info{PieceLength: 2 * defaultChunkSize}
	qt.Assert(t, qt.IsNil(info.BuildFromFilePath(root)))
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	return &metainfo.MetaInfo{
		InfoBytes: infoBytes,
		UrlList:   metainfo.UrlList{srvURL + "/"},
	}
}

func waitForTorrentsGC(t *testing.T, dropped []weak.Pointer[Torrent], timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for i, wp := range dropped {
		for {
			runtime.GC()
			if wp.Value() == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		qt.Check(t, qt.IsNil(wp.Value()), qt.Commentf("%s (torrent %d)", label, i))
	}
}

// Dropped torrents that used webseeds should be garbage collected, even after
// Client.updateWebseedRequests has run and reused Client.heapSlice and Client.roaringIntIterator.
func TestDroppedWebseedTorrentsCollected(t *testing.T) {
	srvDir := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(srvDir)))
	t.Cleanup(srv.Close)

	cfg := TestingConfig(t)
	cfg.DisableWebseeds = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	// Build all MetaInfos up front so the torrents are added and scheduled concurrently in
	// cl.heapSlice.
	mis := make([]*metainfo.MetaInfo, 5)
	for i := range mis {
		mis[i] = makeWebseedTestMetaInfo(t, srvDir, srv.URL, fmt.Sprintf("t%d", i), 4*defaultChunkSize)
	}

	var dropped []weak.Pointer[Torrent]
	func() {
		torrents := make([]*Torrent, 0, len(mis))
		for _, mi := range mis {
			tor, err := cl.AddTorrent(mi)
			qt.Assert(t, qt.IsNil(err))
			<-tor.GotInfo()
			tor.DownloadAll()
			torrents = append(torrents, tor)
			dropped = append(dropped, weak.Make(tor))
		}
		for i, tor := range torrents {
			select {
			case <-tor.Complete().On():
			case <-time.After(10 * time.Second):
				t.Fatalf("torrent %d did not complete", i)
			}
		}
		for _, tor := range torrents {
			tor.Drop()
		}
	}()

	// Run the webseed scheduler once more with no active torrents (same as the periodic timer
	// firing after Drop). This clears cl.aprioriMap at entry, isolating cl.heapSlice and
	// cl.roaringIntIterator retention.
	cl.lock()
	cl.updateWebseedRequests()
	cl.unlock()

	waitForTorrentsGC(t, dropped, 15*time.Second, "torrent still retained after scheduler pass")
}

// A dropped webseed torrent should be collectable right away, without waiting for
// Client.aprioriMap to be cleared by the next periodic webseedRequestTimer tick.
func TestDroppedWebseedTorrentCollectedBeforeNextTick(t *testing.T) {
	srvDir := t.TempDir()
	srv := httptest.NewServer(http.FileServer(http.Dir(srvDir)))
	t.Cleanup(srv.Close)

	cfg := TestingConfig(t)
	cfg.DisableWebseeds = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	mi := makeWebseedTestMetaInfo(t, srvDir, srv.URL, "single", 4*defaultChunkSize)
	var wp weak.Pointer[Torrent]
	func() {
		tor, err := cl.AddTorrent(mi)
		qt.Assert(t, qt.IsNil(err))
		<-tor.GotInfo()
		tor.DownloadAll()
		select {
		case <-tor.Complete().On():
		case <-time.After(10 * time.Second):
			t.Fatal("torrent did not complete")
		}
		// Prevent the periodic webseed timer from firing and masking cl.aprioriMap retention.
		cl.webseedRequestTimer.Stop()
		wp = weak.Make(tor)
		tor.Drop()
	}()

	waitForTorrentsGC(t, []weak.Pointer[Torrent]{wp}, 15*time.Second, "torrent still retained before next webseed timer tick")
}
