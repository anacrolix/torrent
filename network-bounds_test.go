package torrent

import (
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
)

// Returns a conn on the greeting torrent with the client locked. The torrent has its info if
// haveInfo.
func newNetworkBoundsTestConn(t *testing.T, cfg *ClientConfig, haveInfo bool) (*Torrent, *PeerConn) {
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	mi := testutil.GreetingMetaInfo()
	tt, _ := cl.AddTorrentInfoHash(mi.HashInfoBytes())
	cl.lock()
	t.Cleanup(cl.unlock)
	if haveInfo {
		qt.Assert(t, qt.IsNil(tt.setInfoBytesLocked(mi.InfoBytes, nil)))
	}
	ours, theirs := net.Pipe()
	t.Cleanup(func() { theirs.Close() })
	go io.Copy(io.Discard, theirs)
	c := cl.newConnection(ours, newConnectionOpts{network: "test"})
	c.setTorrent(tt)
	c.startMessageWriter()
	t.Cleanup(func() { c.closed.Set() })
	return tt, c
}

// metadata_size used to be checked after truncating to uint32, so sizes that truncate to something
// small passed, and make() panicked (negative) or allocated gigabytes.
func TestExtendedHandshakeBadMetadataSize(t *testing.T) {
	for _, size := range []int64{
		-1,
		-(1 << 32) + 5,
		1<<32 + 5,
		int64(maxMetadataSize) + 1,
	} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tt, c := newNetworkBoundsTestConn(t, TestingConfig(t), false)
			payload := fmt.Appendf(nil, "d13:metadata_sizei%dee", size)
			err := c.onReadExtendedMsg(pp.HandshakeExtendedID, payload)
			qt.Check(t, qt.IsNotNil(err))
			qt.Check(t, qt.Equals(tt.metadataSize(), 0))
		})
	}
}

// Received chunks must match the request exactly. Oversized or misaligned chunks used to be written
// past the end of the piece, panicking in storage.
func TestReceiveChunkMustMatchRequest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		begin pp.Integer
		len   int
	}{
		{"oversized", 0, 100},
		{"misaligned", 1, 4},
		{"short", 0, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt, c := newNetworkBoundsTestConn(t, TestingConfig(t), true)
			qt.Assert(t, qt.Equals(tt.pieceLength(0), pp.Integer(5)))
			ri := tt.requestIndexFromRequest(Request{Index: 0})
			c.validReceiveChunks = map[RequestIndex]int{ri: 1}
			err := c.receiveChunk(&pp.Message{
				Type:  pp.Piece,
				Index: 0,
				Begin: tc.begin,
				Piece: make([]byte, tc.len),
			})
			qt.Check(t, qt.IsNotNil(err))
		})
	}
}

// Hash requests before we have the info used to dereference the nil info.
func TestHashRequestBeforeInfo(t *testing.T) {
	_, c := newNetworkBoundsTestConn(t, TestingConfig(t), false)
	err := c.onHashRequest(&pp.Message{Type: pp.HashRequest, Length: 2})
	qt.Check(t, qt.IsNotNil(err))
}

func TestRequestedChunkLengthKeyBounded(t *testing.T) {
	qt.Check(t, qt.Equals(requestedChunkLengthKey(1<<14), "16384"))
	qt.Check(t, qt.Equals(requestedChunkLengthKey(1<<14+1), "other"))
	qt.Check(t, qt.Equals(requestedChunkLengthKey(0), "other"))
}

// Peers can repeat the extended handshake with new extension names each time.
func TestPeerExtensionNamesBounded(t *testing.T) {
	cfg := TestingConfig(t)
	// Pipe conns have no remote address for PEX.
	cfg.DisablePEX = true
	_, c := newNetworkBoundsTestConn(t, cfg, false)
	for i := range 4 {
		m := make(map[pp.ExtensionName]pp.ExtensionNumber)
		for j := range maxPeerExtensionNames {
			m[pp.ExtensionName(fmt.Sprintf("x%d-%d", i, j))] = 1
		}
		err := c.onReadExtendedMsg(
			pp.HandshakeExtendedID,
			bencode.MustMarshal(pp.ExtendedHandshakeMessage{M: m}))
		qt.Assert(t, qt.IsNil(err))
	}
	qt.Check(t, qt.Equals(len(c.PeerExtensionIDs), maxPeerExtensionNames))
	peersSupportingExtension.mu.Lock()
	defer peersSupportingExtension.mu.Unlock()
	qt.Check(t, qt.IsTrue(len(peersSupportingExtension.keys) <= maxBoundedExpvarMapKeys))
}

// Any peer can send ut_holepunch connects for any address, and they skip the dial rate limiter.
func TestHolepunchConnectRespectsHalfOpenLimit(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.HalfOpenConnsPerTorrent = 2
	tt, c := newNetworkBoundsTestConn(t, cfg, true)
	for i := range 10 {
		err := tt.handleReceivedUtHolepunchMsg(utHolepunch.Msg{
			MsgType:  utHolepunch.Connect,
			AddrPort: netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}), 1),
		}, c)
		qt.Assert(t, qt.IsNil(err))
	}
	qt.Check(t, qt.Equals(len(tt.halfOpen), 2))
}

// The hash request range check must not wrap: index+length is computed in uint32 on the wire.
func TestHashRequestRangeDoesNotWrap(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/v2-multi-piece.torrent")
	qt.Assert(t, qt.IsNil(err))
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	<-tt.GotInfo()
	cl.lock()
	defer cl.unlock()
	c := cl.newConnection(nil, newConnectionOpts{network: "test"})
	c.setTorrent(tt)
	root := tt.Files()[0].piecesRoot.Unwrap()
	_, err = c.getHashes(&pp.Message{PiecesRoot: root, Index: 0, Length: 2})
	qt.Check(t, qt.IsNil(err))
	_, err = c.getHashes(&pp.Message{PiecesRoot: root, Index: math.MaxUint32, Length: 2})
	qt.Check(t, qt.IsNotNil(err))
}

type pingCountingDhtServer struct {
	DhtServer
	pings chan *net.UDPAddr
}

func (me pingCountingDhtServer) Ping(addr *net.UDPAddr) {
	me.pings <- addr
}

// Each Port message used to start a DHT ping. Only the first on a conn should.
func TestPortMessagePingsOnce(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	ds := pingCountingDhtServer{pings: make(chan *net.UDPAddr, 10)}
	tt, _ := cl.AddTorrentInfoHash(testutil.GreetingMetaInfo().HashInfoBytes())
	cl.lock()
	defer cl.unlock()
	cl.dhtServers = append(cl.dhtServers, ds)
	defer func() { cl.dhtServers = nil }()
	c := cl.newConnection(nil, newConnectionOpts{
		network:    "tcp",
		remoteAddr: &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678},
	})
	c.setTorrent(tt)
	for range 3 {
		c.onPeerSentPort(6881)
	}
	select {
	case addr := <-ds.pings:
		qt.Check(t, qt.Equals(addr.String(), "1.2.3.4:6881"))
	case <-time.After(5 * time.Second):
		t.Fatal("no ping")
	}
	time.Sleep(50 * time.Millisecond)
	qt.Check(t, qt.HasLen(ds.pings, 0))
}

// Repeated extended handshakes must not list the conn for PEX again, or replace its PEX timer.
func TestRepeatedExtendedHandshakePex(t *testing.T) {
	tt, c := newNetworkBoundsTestConn(t, TestingConfig(t), false)
	c.RemoteAddr = &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678}
	c.Network = "tcp"
	c.LocalLtepProtocolMap = &tt.cl.defaultLocalLtepProtocolMap
	payload := bencode.MustMarshal(pp.ExtendedHandshakeMessage{
		M:    map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNamePex: 1},
		Port: 5678,
	})
	qt.Assert(t, qt.IsNil(c.onReadExtendedMsg(pp.HandshakeExtendedID, payload)))
	qt.Assert(t, qt.IsTrue(c.pex.IsEnabled()))
	timer := c.pex.timer
	for range 10 {
		qt.Assert(t, qt.IsNil(c.onReadExtendedMsg(pp.HandshakeExtendedID, payload)))
	}
	qt.Check(t, qt.Equals(c.pex.timer, timer))
	qt.Check(t, qt.Equals(tt.pex.nc, 1))
	qt.Check(t, qt.Equals(tt.pex.numPending(nil), 1))
}
