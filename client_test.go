package torrent

import (
	"os"
	"testing"
	"time"

	"bitbucket.org/anacrolix/go.torrent/testutil"
	"bitbucket.org/anacrolix/go.torrent/util"
	"github.com/anacrolix/libtorgo/bencode"
)

func TestClientDefault(t *testing.T) {
	cl, err := NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	cl.Stop()
}

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
	if pieceHash.Size() != 20 {
		t.FailNow()
	}
}

func TestTorrentInitialState(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tor, err := newTorrent(func() (ih InfoHash) {
		util.CopyExact(ih[:], mi.Info.Hash)
		return
	}(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = tor.setMetadata(mi.Info.Info, dir, mi.Info.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(tor.Pieces) != 1 {
		t.Fatal("wrong number of pieces")
	}
	p := tor.Pieces[0]
	if len(p.PendingChunkSpecs) != 1 {
		t.Fatalf("should only be 1 chunk: %v", p.PendingChunkSpecs)
	}
	if _, ok := p.PendingChunkSpecs[chunkSpec{
		Length: 13,
	}]; !ok {
		t.Fatal("pending chunk spec is incorrect")
	}
}

func TestUnmarshalPEXMsg(t *testing.T) {
	var m peerExchangeMessage
	if err := bencode.Unmarshal([]byte("d5:added12:\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0ce"), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Added) != 2 {
		t.FailNow()
	}
	if m.Added[0].Port != 0x506 {
		t.FailNow()
	}
}

func TestReducedDialTimeout(t *testing.T) {
	for _, _case := range []struct {
		Max             time.Duration
		HalfOpenLimit   int
		PendingPeers    int
		ExpectedReduced time.Duration
	}{
		{dialTimeout, 40, 0, dialTimeout},
		{dialTimeout, 40, 1, dialTimeout},
		{dialTimeout, 40, 39, dialTimeout},
		{dialTimeout, 40, 40, dialTimeout / 2},
		{dialTimeout, 40, 80, dialTimeout / 3},
		{dialTimeout, 40, 4000, dialTimeout / 101},
	} {
		reduced := reducedDialTimeout(_case.Max, _case.HalfOpenLimit, _case.PendingPeers)
		if reduced != _case.ExpectedReduced {
			t.Fatalf("expected %s, got %s", _case.ExpectedReduced, reduced)
		}
	}
}
