package torrent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	qt "github.com/go-quicktest/qt"
)

// Dropping a torrent while one of its webseed requests is still in flight
// must not make the next webseed scheduling pass panic: the torrent leaves
// cl.torrents at once, but its requests leave cl.activeWebseedRequests only
// when they close.
func TestUpdateWebseedRequestsAfterDrop(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Add(1)
		<-release // hold the request open so it stays in flight
	}))
	t.Cleanup(srv.Close)

	src := t.TempDir()
	root := filepath.Join(src, "t")
	qt.Assert(t, qt.IsNil(os.MkdirAll(root, 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "f.bin"), make([]byte, 1<<20), 0o644)))
	var info metainfo.Info
	qt.Assert(t, qt.IsNil(info.BuildFromFilePath(root)))
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes, UrlList: metainfo.UrlList{srv.URL + "/"}}

	cfg := TestingConfig(t)
	cfg.DisableWebseeds = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	defer close(release) // let the held request finish before the client closes
	tor, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tor.GotInfo()
	tor.DownloadAll()
	for deadline := time.Now().Add(10 * time.Second); started.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no webseed request was issued")
		}
	}

	// Drop and run the scheduler under one lock hold, so the in-flight
	// request cannot remove itself in between and the window is certain.
	// The lock is released even if the scheduler panics, so a failure is a
	// clean test failure rather than a deadlocked Close.
	var wg sync.WaitGroup
	panicked := func() (r any) {
		defer func() { r = recover() }()
		cl.lock()
		defer cl.unlock()
		cl.dropTorrent(tor, &wg)
		cl.updateWebseedRequests()
		return nil
	}()
	if panicked != nil {
		t.Fatalf("updateWebseedRequests panicked after Drop: %v", panicked)
	}
	wg.Wait()
}
