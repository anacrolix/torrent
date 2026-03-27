package torrent

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/internal/testutil"
)

// Tests that the client can download a multi-file torrent from two webseeds simultaneously when
// each webseed only has one of the two files (non-overlapping data). The piece bitmaps are
// restricted to match the files each server has, and webseedRequestChunkSize is set to pieceLen so
// each piece is its own webseed slice (preventing requests from spanning into the other server's
// data due to the alignment of getWebseedRequestEnd).
func TestDownloadFromTwoNonOverlappingWebseeds(t *testing.T) {
	// Override webseedRequestChunkSize so each piece maps to its own slice, ensuring requests stay
	// within the pieces each webseed has.
	const pieceLen = 2 * defaultChunkSize // 32 KiB; two 16 KiB chunks per piece
	old := webseedRequestChunkSize
	webseedRequestChunkSize = uint64(pieceLen)
	defer func() { webseedRequestChunkSize = old }()

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
	mi, info := tu.Generate(int64(pieceLen))
	numPieces := info.NumPieces() // 4
	mid := numPieces / 2          // 2; pieces 0-1 = a.bin, pieces 2-3 = b.bin

	// Server 1: serves only a.bin; b.bin returns 404 naturally.
	dir1 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir1, "testdata"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "testdata", "a.bin"), dataA, 0o644))
	srv1 := httptest.NewServer(http.FileServer(http.Dir(dir1)))
	defer srv1.Close()

	// Server 2: serves only b.bin; a.bin returns 404 naturally.
	dir2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir2, "testdata"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "testdata", "b.bin"), dataB, 0o644))
	srv2 := httptest.NewServer(http.FileServer(http.Dir(dir2)))
	defer srv2.Close()

	cfg := TestingConfig(t)
	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	// BEP 19 multi-file webseeds use a trailing slash; the file path is appended automatically.
	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  mi.HashInfoBytes(),
			InfoBytes: mi.InfoBytes,
		},
		Webseeds: []string{srv1.URL + "/", srv2.URL + "/"},
	})
	require.NoError(t, err)

	// Restrict each webseed to only the pieces of its file. Remove pieces that the webseed doesn't
	// have and decrement their availability counts, so the scheduler doesn't assign wrong pieces
	// (which would cause a conviction when the missing file returns 404).
	cl.lock()
	for _, ws := range tt.webSeeds {
		var removeStart, removeEnd int
		if strings.HasPrefix(ws.client.Url, srv1.URL) {
			removeStart, removeEnd = mid, numPieces // keep [0, mid)
		} else {
			removeStart, removeEnd = 0, mid // keep [mid, numPieces)
		}
		for i := removeStart; i < removeEnd; i++ {
			ws.client.Pieces.Remove(uint32(i))
			tt.decPieceAvailability(pieceIndex(i))
		}
	}
	cl.unlock()

	tt.DownloadAll()
	require.True(t, cl.WaitAll())

	r := tt.NewReader()
	defer r.Close()
	got, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, append(dataA, dataB...), got)
}
