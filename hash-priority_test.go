package torrent

import (
	"testing"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Two torrents with several pieces each, and no hashers running until the test asks.
func newHashPriorityClient(t *testing.T) (cl *Client, checking, downloaded *Torrent) {
	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: t.TempDir()})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	setMaxActivePieceHashers(t, cl, 0)
	add := func(name string) *Torrent {
		mi, err := metainfo.LoadFromFile(name)
		qt.Assert(t, qt.IsNil(err))
		tt, err := cl.AddTorrent(mi)
		qt.Assert(t, qt.IsNil(err))
		<-tt.GotInfo()
		return tt
	}
	checking = add("testdata/debian-10.8.0-amd64-netinst.iso.torrent")
	downloaded = add("testdata/sintel.torrent")
	cl.lock()
	for _, tt := range []*Torrent{checking, downloaded} {
		tt.piecesQueuedForHash.Clear()
		tt.piecesDownloadedForHash.Clear()
	}
	checking.piecesQueuedForHash.AddRange(0, 3)
	downloaded.piecesQueuedForHash.AddRange(0, 3)
	downloaded.piecesDownloadedForHash.Add(2)
	maxActivePieceHashers = 1
	cl.unlock()
	return
}

func TestDownloadedPiecesAreHashedFirst(t *testing.T) {
	cl, checking, downloaded := newHashPriorityClient(t)
	cl.lock()
	defer cl.unlock()
	qt.Check(t, qt.Equals(downloaded.getPieceToHash(), g.Some(2)))
	qt.Check(t, qt.IsTrue(cl.downloadedPiecesWaitForHash(checking)))
	qt.Check(t, qt.IsFalse(cl.downloadedPiecesWaitForHash(downloaded)))

	// The only hasher goes to the torrent with a downloaded piece, though the other is larger.
	qt.Assert(t, qt.IsTrue(checking.length() > downloaded.length()))
	cl.startPieceHashers()
	qt.Check(t, qt.Equals(downloaded.activePieceHashes, 1))
	qt.Check(t, qt.Equals(checking.activePieceHashes, 0))
	qt.Check(t, qt.IsTrue(downloaded.piecesDownloadedForHash.IsEmpty()))
}
