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

// A slice of the torrent must be fetched from one webseed at a time. Requests used to be tracked
// per webseed and slice, so every webseed was asked for the same slice at once: the copies fetched
// the same bytes, and whichever copy read a chunk another had already delivered cancelled itself.
func TestWebseedSliceIsNotRequestedFromEveryWebseed(t *testing.T) {
	const pieceLen = 2 * defaultChunkSize // two chunks per piece

	// Small slices, so a small torrent still has several of them.
	defer func(v uint64) { webseedRequestChunkSize = v }(webseedRequestChunkSize)
	webseedRequestChunkSize = 4 * pieceLen

	data := make([]byte, 16*pieceLen)
	rand.Read(data)
	tu := testutil.Torrent{
		Name:  "testdata",
		Files: []testutil.File{{Name: "a.bin", Data: string(data)}},
	}
	mi, _ := tu.Generate(int64(pieceLen))

	dir := t.TempDir()
	qt.Assert(t, qt.IsNil(os.MkdirAll(filepath.Join(dir, "testdata"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(dir, "testdata", "a.bin"), data, 0o644)))

	var (
		mu       sync.Mutex
		inFlight = map[string]int{} // range header -> number of servers serving it right now
		overlaps int
		served   int64
	)
	// Every webseed serves the whole torrent, so all of them are candidates for every slice. The
	// delay keeps requests in flight long enough to overlap.
	var urls []string
	for range 4 {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rng := r.Header.Get("Range")
			mu.Lock()
			inFlight[rng]++
			if inFlight[rng] > 1 {
				overlaps++
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			rec := httptest.NewRecorder()
			http.FileServer(http.Dir(dir)).ServeHTTP(rec, r)
			mu.Lock()
			inFlight[rng]--
			served += int64(rec.Body.Len())
			mu.Unlock()
			for k, vs := range rec.Header() {
				w.Header()[k] = vs
			}
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
		}))
		defer srv.Close()
		urls = append(urls, srv.URL+"/")
	}

	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{InfoHash: mi.HashInfoBytes(), InfoBytes: mi.InfoBytes},
		Webseeds:       urls,
	})
	qt.Assert(t, qt.IsNil(err))
	tt.DownloadAll()
	qt.Assert(t, qt.IsTrue(cl.WaitAll()))

	mu.Lock()
	defer mu.Unlock()
	qt.Assert(t, qt.Equals(overlaps, 0))
	// Without the duplicate requests the webseeds serve about the torrent's own size.
	qt.Assert(t, qt.IsTrue(served < int64(len(data))*2))
}
