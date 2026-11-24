package torrent

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

// Web seed URLs that don't parse are ignored.
func TestWebseedBadUrlIgnored(t *testing.T) {
	tu := testutil.Torrent{
		Name:  "testdata",
		Files: []testutil.File{{Name: "a.bin", Data: "hello"}},
	}
	mi, _ := tu.Generate(defaultChunkSize)
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, _, err := cl.AddTorrentSpec(&TorrentSpec{
		AddTorrentOpts: AddTorrentOpts{
			InfoHash:  mi.HashInfoBytes(),
			InfoBytes: mi.InfoBytes,
		},
	})
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	tt.AddWebSeeds([]string{"http://[::1/"})
	qt.Assert(t, qt.HasLen(tt.WebseedPeerConns(), 0))
}
