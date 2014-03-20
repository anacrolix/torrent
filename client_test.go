package torrent

import (
	"os"

	"bitbucket.org/anacrolix/go.torrent/testutil"

	"testing"
)

func TestAddTorrentNoSupportedTrackerSchemes(t *testing.T) {
	t.SkipNow()
}

func TestAddTorrentNoUsableURLs(t *testing.T) {
	t.SkipNow()
}

func TestAddPeersToUnknownTorrent(t *testing.T) {
	t.SkipNow()
}

func TestPieceHashSize(t *testing.T) {
	if PieceHash.Size() != 20 {
		t.FailNow()
	}
}

func TestTorrentInitialState(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tor, err := newTorrent(mi, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tor.Pieces) != 1 {
		t.Fatal("wrong number of pieces")
	}
	p := tor.Pieces[0]
	if len(p.PendingChunkSpecs) != 1 {
		t.Fatalf("should only be 1 chunk: %s", p.PendingChunkSpecs)
	}
	if _, ok := p.PendingChunkSpecs[ChunkSpec{
		Length: 13,
	}]; !ok {
		t.Fatal("pending chunk spec is incorrect")
	}
}
