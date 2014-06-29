package torrent

import (
	"github.com/anacrolix/libtorgo/bencode"
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
	if pieceHash.Size() != 20 {
		t.FailNow()
	}
}

func TestTorrentInitialState(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tor, err := newTorrent(BytesInfoHash(mi.InfoHash), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = tor.setMetadata(mi.Info, dir, mi.InfoBytes)
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
	if _, ok := p.PendingChunkSpecs[chunkSpec{
		Length: 13,
	}]; !ok {
		t.Fatal("pending chunk spec is incorrect")
	}
}

func TestUnmarshalCompactPeer(t *testing.T) {
	var p Peer
	err := bencode.Unmarshal([]byte("6:\x01\x02\x03\x04\x05\x06"), &p)
	if err != nil {
		t.Fatal(err)
	}
	if p.IP.String() != "1.2.3.4" {
		t.FailNow()
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
