// BEP 27 private-torrent semantics.
// http://www.bittorrent.org/beps/bep_0027.html
package torrent

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

// makeTorrentWithPrivate is a focused helper for the BEP 27 tests below.
// It builds a *Torrent shell carrying just the info needed by isPrivate(),
// without touching the network or storage. The Torrent is not registered
// with a Client and must not be used for any other operations.
func makeTorrentWithPrivate(p *bool) *Torrent {
	return &Torrent{
		info: &metainfo.Info{Private: p},
	}
}

func TestIsPrivate_NoInfo(t *testing.T) {
	// Before metadata is loaded (e.g. magnet still fetching), isPrivate
	// returns false. The downstream loops re-check on every iteration,
	// so once info arrives the privacy flag takes effect immediately.
	tor := &Torrent{}
	if tor.isPrivate() {
		t.Fatal("torrent with no info should not report private")
	}
}

func TestIsPrivate_FlagAbsent(t *testing.T) {
	// Most public torrents omit the field entirely (Info.Private == nil).
	tor := makeTorrentWithPrivate(nil)
	if tor.isPrivate() {
		t.Fatal("info without private field should not report private")
	}
}

func TestIsPrivate_FlagFalse(t *testing.T) {
	// Explicit `private=0` must behave the same as omitted.
	f := false
	tor := makeTorrentWithPrivate(&f)
	if tor.isPrivate() {
		t.Fatal("private=0 should not report private")
	}
}

func TestIsPrivate_FlagTrue(t *testing.T) {
	// The actual private case.
	tt := true
	tor := makeTorrentWithPrivate(&tt)
	if !tor.isPrivate() {
		t.Fatal("private=1 should report private")
	}
}
