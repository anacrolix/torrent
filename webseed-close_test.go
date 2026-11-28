package torrent

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

func addTorrentWithWebseedForCloseTest(t *testing.T) (*Client, *Torrent) {
	const pieceLen = 2 * defaultChunkSize
	data := make([]byte, 3*pieceLen)
	rand.Read(data)
	tu := testutil.Torrent{
		Name:  "testdata",
		Files: []testutil.File{{Name: "a.bin", Data: string(data)}},
	}
	mi, _ := tu.Generate(int64(pieceLen))
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  mi.HashInfoBytes(),
			InfoBytes: mi.InfoBytes,
		},
	})
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	tt.AddWebSeeds([]string{srv.URL + "/"})
	return cl, tt
}

// Closing a web seed through the handle Torrent.WebseedPeerConns gives out removes it from the
// Torrent, like Torrent.RemoveWebSeeds does. It used to stay, so requests were still planned for
// it, and each failed at once on its cancelled context.
func TestClosingWebseedRemovesItFromTorrent(t *testing.T) {
	cl, tt := addTorrentWithWebseedForCloseTest(t)
	peers := tt.WebseedPeerConns()
	qt.Assert(t, qt.HasLen(peers, 1))
	qt.Assert(t, qt.IsNil(peers[0].Close()))
	qt.Check(t, qt.HasLen(tt.WebseedPeerConns(), 0))
	cl.lock()
	defer cl.unlock()
	for i := range tt.pieces {
		qt.Check(t, qt.Equals(tt.pieces[i].relativeAvailability, 0), qt.Commentf("piece %v", i))
	}
}

// A request can learn that the web seed doesn't have a file after the web seed was closed. Closing
// already gave back the availability of the web seed's pieces, and giving it back again panicked.
func TestWebseedFileUnavailableAfterClose(t *testing.T) {
	cl, tt := addTorrentWithWebseedForCloseTest(t)
	cl.lock()
	defer cl.unlock()
	qt.Assert(t, qt.HasLen(tt.webSeeds, 1))
	for _, ws := range tt.webSeeds {
		ws.peer.close()
		ws.removeFilePieces(0)
	}
	for i := range tt.pieces {
		qt.Check(t, qt.Equals(tt.pieces[i].relativeAvailability, 0), qt.Commentf("piece %v", i))
	}
}
