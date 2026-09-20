package torrent

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

// Tests that the client can download a multi-file torrent from two webseeds simultaneously when
// each webseed only has one of the two files (non-overlapping data). When a webseed receives a 404
// for a file it doesn't have, the pieces for that file are removed from its bitmap and the
// scheduler reassigns them to the other webseed.
func TestDownloadFromTwoNonOverlappingWebseeds(t *testing.T) {
	const pieceLen = 2 * defaultChunkSize // 32 KiB; two 16 KiB chunks per piece

	// Two files, each spanning exactly 2 pieces.
	fileLen := 2 * pieceLen
	dataA := make([]byte, fileLen)
	dataB := make([]byte, fileLen)
	rand.Read(dataA)
	rand.Read(dataB)

	tu := testutil.Torrent{
		Name: "testdata",
		Files: []testutil.File{
			{Name: "a.bin", Data: string(dataA)},
			{Name: "b.bin", Data: string(dataB)},
		},
	}
	mi, _ := tu.Generate(int64(pieceLen))

	// Server 1: serves only a.bin; b.bin returns 404 naturally.
	dir1 := t.TempDir()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir1, "testdata"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir1, "testdata", "a.bin"), dataA, 0o644)))
	srv1 := httptest.NewServer(http.FileServer(http.Dir(dir1)))
	defer srv1.Close()

	// Server 2: serves only b.bin; a.bin returns 404 naturally.
	dir2 := t.TempDir()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir2, "testdata"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir2, "testdata", "b.bin"), dataB, 0o644)))
	srv2 := httptest.NewServer(http.FileServer(http.Dir(dir2)))
	defer srv2.Close()

	cfg := TestingConfig(t)
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	// BEP 19 multi-file webseeds use a trailing slash; the file path is appended automatically.
	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  mi.HashInfoBytes(),
			InfoBytes: mi.InfoBytes,
		},
		Webseeds: []string{srv1.URL + "/", srv2.URL + "/"},
	})
	qt.Assert(t, qt.IsNil(err))

	tt.DownloadAll()
	qt.Assert(t, qt.IsTrue(cl.WaitAll()))

	r := tt.NewReader()
	defer r.Close()
	got, err := io.ReadAll(r)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(got, append(dataA, dataB...)))
}

// Regression test for https://github.com/anacrolix/torrent/issues/1098: cl.activeWebseedRequests
// (the Client-level view of in-flight webseed requests) and the per-torrent view built by walking
// cl.torrents can transiently disagree after a torrent with in-flight webseed requests is dropped,
// because the torrent leaves cl.torrents synchronously while its entries leave
// cl.activeWebseedRequests only once each request notices it was cancelled and closes
// asynchronously. Client.updateWebseedRequests must tolerate that divergence instead of panicking.
func TestDropTorrentWithInFlightWebseedRequests(t *testing.T) {
	// The handler blocks until the test is done, keeping any webseed request that reaches it
	// in-flight for the duration of the test.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	data := make([]byte, 4*defaultChunkSize)
	rand.Read(data)
	tu := testutil.Torrent{
		Name:  "testdata",
		Files: []testutil.File{{Name: "a.bin", Data: string(data)}},
	}
	mi, _ := tu.Generate(int64(2 * defaultChunkSize))

	cfg := TestingConfig(t)
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  mi.HashInfoBytes(),
			InfoBytes: mi.InfoBytes,
		},
		Webseeds: []string{srv.URL + "/"},
	})
	qt.Assert(t, qt.IsNil(err))
	tt.DownloadAll()

	// Wait until there's a webseed request in flight (blocked in the handler above).
	deadline := time.Now().Add(10 * time.Second)
	for {
		cl.rLock()
		n := len(cl.activeWebseedRequests)
		cl.rUnlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a webseed request to start")
		}
		time.Sleep(time.Millisecond)
	}

	// Race dropping the torrent (which cancels, but doesn't synchronously remove, its in-flight
	// webseed requests) against simulated timer firings, which used to panic (see gh-1098).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			cl.lock()
			cl.updateWebseedRequests()
			cl.unlock()
		}
	}()
	tt.Drop()
	close(stop)
	wg.Wait()
}
