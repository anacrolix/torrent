package torrent

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
