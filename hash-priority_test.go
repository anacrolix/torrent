package torrent

import (
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestDownloadedPiecesAreHashedFirst(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: t.TempDir()})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	add := func(name string) *Torrent {
		mi, err := metainfo.LoadFromFile(name)
		qt.Assert(t, qt.IsNil(err))
		tt, err := cl.AddTorrent(mi)
		qt.Assert(t, qt.IsNil(err))
		<-tt.GotInfo()
		return tt
	}
	checking := add("testdata/debian-10.8.0-amd64-netinst.iso.torrent")
	downloaded := add("testdata/sintel.torrent")

	// Let the checks from adding the torrents finish. Then queue by hand and look under the same
	// lock, so no hasher gets to the pieces meanwhile.
	deadline := time.Now().Add(10 * time.Second)
	for {
		cl.lock()
		if cl.activePieceHashers == 0 || time.Now().After(deadline) {
			break
		}
		cl.unlock()
		time.Sleep(time.Millisecond)
	}
	defer cl.unlock()
	qt.Assert(t, qt.Equals(cl.activePieceHashers, 0))
	checking.piecesQueuedForHash.AddRange(0, 3)
	downloaded.piecesQueuedForHash.AddRange(0, 3)
	downloaded.piecesDownloadedForHash.Add(2)

	qt.Check(t, qt.Equals(downloaded.getPieceToHash(), g.Some(2)))
	qt.Check(t, qt.IsTrue(cl.downloadedPiecesWaitForHash(checking)))
	qt.Check(t, qt.IsFalse(cl.downloadedPiecesWaitForHash(downloaded)))
	// Hashers go to the torrent with a downloaded piece first, though the other is larger.
	qt.Assert(t, qt.IsTrue(checking.length() > downloaded.length()))
	qt.Check(t, qt.IsTrue(hashFirst(downloaded, checking)))
	qt.Check(t, qt.IsFalse(hashFirst(checking, downloaded)))
	// Without downloaded pieces, the larger torrent goes first as before.
	downloaded.piecesDownloadedForHash.Clear()
	qt.Check(t, qt.IsTrue(hashFirst(checking, downloaded)))
}
