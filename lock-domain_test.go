package torrent

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

func TestLockDomainsFollowStorageCapacity(t *testing.T) {
	cl := new(Client)
	cl.init(TestingConfig(t))
	f := func() (int64, bool) { return 0, false }
	capacity := storage.TorrentCapacity(&f)
	addTorrent := func(i byte, capacity storage.TorrentCapacity) *Torrent {
		ih := testingTorrentInfoHash
		ih[19] = i
		tor, _ := cl.AddTorrentOpt(AddTorrentOpts{
			InfoHash: ih,
			Storage:  &torrentStorageClient{ts: &torrentStorage{}, capacity: capacity},
		})
		qt.Assert(t, qt.IsNil(tor.setInfoUnlocked(&metainfo.Info{
			Pieces:      make([]byte, 20),
			Length:      1,
			PieceLength: 1,
		})))
		return tor
	}
	shared1 := addTorrent(1, capacity)
	shared2 := addTorrent(2, capacity)
	own1 := addTorrent(3, nil)
	own2 := addTorrent(4, nil)
	qt.Check(t, qt.Equals(shared1.domain, shared2.domain))
	qt.Check(t, qt.Not(qt.Equals(own1.domain, own2.domain)))
	qt.Check(t, qt.Not(qt.Equals(own1.domain, shared1.domain)))
}

func TestDeferExclusive(t *testing.T) {
	cl := new(Client)
	cl.init(TestingConfig(t))
	tor, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHash: testingTorrentInfoHash})
	var ran []string
	tor.lockDomain()
	qt.Assert(t, qt.IsTrue(tor.inDomainMode()))
	tor.deferExclusive(func() {
		cl.assertExclusive()
		ran = append(ran, "a")
	})
	tor.deferExclusiveOnce(1, func() { ran = append(ran, "b") })
	tor.deferExclusiveOnce(1, func() { ran = append(ran, "c") })
	qt.Check(t, qt.HasLen(ran, 0))
	tor.unlockDomain()
	qt.Check(t, qt.DeepEquals(ran, []string{"a", "b"}))
	// In exclusive mode the action runs immediately.
	cl.lock()
	qt.Check(t, qt.IsFalse(tor.inDomainMode()))
	tor.deferExclusive(func() { ran = append(ran, "d") })
	qt.Check(t, qt.DeepEquals(ran, []string{"a", "b", "d"}))
	cl.unlock()
}
