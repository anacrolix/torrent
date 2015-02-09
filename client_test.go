package torrent

import (
	"fmt"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"bitbucket.org/anacrolix/go.torrent/testutil"
	"bitbucket.org/anacrolix/go.torrent/util"
	"bitbucket.org/anacrolix/utp"
	"github.com/anacrolix/libtorgo/bencode"
)

func TestClientDefault(t *testing.T) {
	cl, err := NewClient(&Config{
		NoDefaultBlocklist: true,
		ListenAddr:         ":0",
	})
	if err != nil {
		t.Fatal(err)
	}
	cl.Stop()
}

func TestAddDropTorrent(t *testing.T) {
	cl, err := NewClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Stop()
	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tt, err := cl.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	tt.Drop()
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
	err = tor.setMetadata(mi.Info.Info, dir, mi.Info.Bytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tor.Pieces) != 1 {
		t.Fatal("wrong number of pieces")
	}
	p := tor.Pieces[0]
	tor.pendAllChunkSpecs(0)
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
		{nominalDialTimeout, 40, 0, nominalDialTimeout},
		{nominalDialTimeout, 40, 1, nominalDialTimeout},
		{nominalDialTimeout, 40, 39, nominalDialTimeout},
		{nominalDialTimeout, 40, 40, nominalDialTimeout / 2},
		{nominalDialTimeout, 40, 80, nominalDialTimeout / 3},
		{nominalDialTimeout, 40, 4000, nominalDialTimeout / 101},
	} {
		reduced := reducedDialTimeout(_case.Max, _case.HalfOpenLimit, _case.PendingPeers)
		expected := _case.ExpectedReduced
		if expected < minDialTimeout {
			expected = minDialTimeout
		}
		if reduced != expected {
			t.Fatalf("expected %s, got %s", _case.ExpectedReduced, reduced)
		}
	}
}

func TestUTPRawConn(t *testing.T) {
	l, err := utp.NewSocket("")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			_, err := l.Accept()
			if err != nil {
				break
			}
		}
	}()
	// Connect a UTP peer to see if the RawConn will still work.
	utpPeer, err := func() *utp.Socket {
		s, _ := utp.NewSocket("")
		return s
	}().Dial(fmt.Sprintf("localhost:%d", util.AddrPort(l.Addr())))
	if err != nil {
		t.Fatalf("error dialing utp listener: %s", err)
	}
	defer utpPeer.Close()
	peer, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	msgsReceived := 0
	const N = 5000 // How many messages to send.
	readerStopped := make(chan struct{})
	// The reader goroutine.
	go func() {
		defer close(readerStopped)
		b := make([]byte, 500)
		for i := 0; i < N; i++ {
			n, _, err := l.PacketConn().ReadFrom(b)
			if err != nil {
				t.Fatalf("error reading from raw conn: %s", err)
			}
			msgsReceived++
			var d int
			fmt.Sscan(string(b[:n]), &d)
			if d != i {
				log.Printf("got wrong number: expected %d, got %d", i, d)
			}
		}
	}()
	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("localhost:%d", util.AddrPort(l.Addr())))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < N; i++ {
		_, err := peer.WriteTo([]byte(fmt.Sprintf("%d", i)), udpAddr)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Microsecond)
	}
	select {
	case <-readerStopped:
	case <-time.After(time.Second):
		t.Fatal("reader timed out")
	}
	if msgsReceived != N {
		t.Fatalf("messages received: %d", msgsReceived)
	}
}

func TestTwoClientsArbitraryPorts(t *testing.T) {
	for i := 0; i < 2; i++ {
		cl, err := NewClient(&Config{
			ListenAddr: ":0",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Stop()
	}
}
