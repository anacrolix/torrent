package torrent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/frankban/quicktest"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

// Ensure that no race exists between sending a bitfield, and a subsequent
// Have that would potentially alter it.
func TestSendBitfieldThenHave(t *testing.T) {
	var cl Client
	cl.init(TestingConfig(t))
	cl.initLogger()
	qtc := qt.New(t)
	c := cl.newConnection(nil, newConnectionOpts{network: "io.Pipe"})
	c.setTorrent(cl.newTorrentForTesting())
	err := c.t.setInfo(&metainfo.Info{Pieces: make([]byte, metainfo.HashSize*3)})
	qtc.Assert(err, qt.IsNil)
	r, w := io.Pipe()
	// c.r = r
	c.w = w
	c.startMessageWriter()
	c.locker().Lock()
	c.t._completedPieces.Add(1)
	c.postBitfield( /*[]bool{false, true, false}*/ )
	c.locker().Unlock()
	c.locker().Lock()
	c.have(2)
	c.locker().Unlock()
	b := make([]byte, 15)
	n, err := io.ReadFull(r, b)
	c.locker().Lock()
	// This will cause connection.writer to terminate.
	c.closed.Set()
	c.locker().Unlock()
	require.NoError(t, err)
	require.EqualValues(t, 15, n)
	// Here we see that the bitfield doesn't have piece 2 set, as that should
	// arrive in the following Have message.
	require.EqualValues(t, "\x00\x00\x00\x02\x05@\x00\x00\x00\x05\x04\x00\x00\x00\x02", string(b))
}

type torrentStorage struct {
	allChunksWritten sync.WaitGroup
}

func (me *torrentStorage) Close() error { return nil }

func (me *torrentStorage) Piece(mp metainfo.Piece) storage.PieceImpl {
	return me
}

func (me *torrentStorage) Completion() storage.Completion {
	return storage.Completion{}
}

func (me *torrentStorage) MarkComplete() error {
	return nil
}

func (me *torrentStorage) MarkNotComplete() error {
	return nil
}

func (me *torrentStorage) ReadAt([]byte, int64) (int, error) {
	panic("shouldn't be called")
}

func (me *torrentStorage) WriteAt(b []byte, _ int64) (int, error) {
	if len(b) != defaultChunkSize {
		panic(len(b))
	}
	me.allChunksWritten.Done()
	return len(b), nil
}

func BenchmarkConnectionMainReadLoop(b *testing.B) {
	c := quicktest.New(b)
	var cl Client
	cl.init(&ClientConfig{
		DownloadRateLimiter: unlimited,
	})
	cl.initLogger()
	ts := &torrentStorage{}
	t := cl.newTorrentForTesting()
	t.initialPieceCheckDisabled = true
	require.NoError(b, t.setInfo(&metainfo.Info{
		Pieces:      make([]byte, 20),
		Length:      1 << 20,
		PieceLength: 1 << 20,
	}))
	t.storage = &storage.Torrent{TorrentImpl: storage.TorrentImpl{Piece: ts.Piece, Close: ts.Close}}
	t.onSetInfo()
	t._pendingPieces.Add(0)
	r, w := net.Pipe()
	c.Logf("pipe reader remote addr: %v", r.RemoteAddr())
	cn := cl.newConnection(r, newConnectionOpts{
		outgoing: true,
		// TODO: This is a hack to give the pipe a bannable remote address.
		remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, 4}), 1234),
		network:    r.RemoteAddr().Network(),
		connString: regularNetConnPeerConnConnString(r),
	})
	c.Assert(cn.bannableAddr.Ok, qt.IsTrue)
	cn.setTorrent(t)
	requestIndexBegin := t.pieceRequestIndexOffset(0)
	requestIndexEnd := t.pieceRequestIndexOffset(1)
	eachRequestIndex := func(f func(ri RequestIndex)) {
		for ri := requestIndexBegin; ri < requestIndexEnd; ri++ {
			f(ri)
		}
	}
	const chunkSize = defaultChunkSize
	numRequests := requestIndexEnd - requestIndexBegin
	msgBufs := make([][]byte, 0, numRequests)
	eachRequestIndex(func(ri RequestIndex) {
		msgBufs = append(msgBufs, pp.Message{
			Type:  pp.Piece,
			Piece: make([]byte, chunkSize),
			Begin: pp.Integer(chunkSize) * pp.Integer(ri),
		}.MustMarshalBinary())
	})
	// errgroup can't handle this pattern...
	allErrors := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cl.lock()
		err := cn.mainReadLoop()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		allErrors <- err
	}()
	b.SetBytes(chunkSize * int64(numRequests))
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < b.N; i += 1 {
			cl.lock()
			// The chunk must be written to storage everytime, to ensure the
			// writeSem is unlocked.
			t.pendAllChunkSpecs(0)
			g.MakeMapIfNil(&cn.validReceiveChunks)
			eachRequestIndex(func(ri RequestIndex) {
				cn.validReceiveChunks[ri] = 1
			})
			cl.unlock()
			ts.allChunksWritten.Add(int(numRequests))
			for _, wb := range msgBufs {
				n, err := w.Write(wb)
				require.NoError(b, err)
				require.EqualValues(b, len(wb), n)
			}
			// This is unlocked by a successful write to storage. So this unblocks when that is
			// done.
			ts.allChunksWritten.Wait()
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
	}()
	go func() {
		wg.Wait()
		close(allErrors)
	}()
	var err error
	for err = range allErrors {
		if err != nil {
			break
		}
	}
	c.Assert(err, qt.IsNil)
	c.Assert(cn._stats.ChunksReadUseful.Int64(), quicktest.Equals, int64(b.N)*int64(numRequests))
	c.Assert(t.smartBanCache.HasBlocks(), qt.IsTrue)
}

func TestConnPexPeerFlags(t *testing.T) {
	var (
		tcpAddr = &net.TCPAddr{IP: net.IPv6loopback, Port: 4848}
		udpAddr = &net.UDPAddr{IP: net.IPv6loopback, Port: 4848}
	)
	testcases := []struct {
		conn *PeerConn
		f    pp.PexPeerFlags
	}{
		{&PeerConn{Peer: Peer{outgoing: false, PeerPrefersEncryption: false}}, 0},
		{&PeerConn{Peer: Peer{outgoing: false, PeerPrefersEncryption: true}}, pp.PexPrefersEncryption},
		{&PeerConn{Peer: Peer{outgoing: true, PeerPrefersEncryption: false}}, pp.PexOutgoingConn},
		{&PeerConn{Peer: Peer{outgoing: true, PeerPrefersEncryption: true}}, pp.PexOutgoingConn | pp.PexPrefersEncryption},
		{&PeerConn{Peer: Peer{RemoteAddr: udpAddr, Network: udpAddr.Network()}}, pp.PexSupportsUtp},
		{&PeerConn{Peer: Peer{RemoteAddr: udpAddr, Network: udpAddr.Network(), outgoing: true}}, pp.PexOutgoingConn | pp.PexSupportsUtp},
		{&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network(), outgoing: true}}, pp.PexOutgoingConn},
		{&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network()}}, 0},
	}
	for i, tc := range testcases {
		f := tc.conn.pexPeerFlags()
		require.EqualValues(t, tc.f, f, i)
	}
}

func TestConnPexEvent(t *testing.T) {
	c := qt.New(t)
	var (
		udpAddr     = &net.UDPAddr{IP: net.IPv6loopback, Port: 4848}
		tcpAddr     = &net.TCPAddr{IP: net.IPv6loopback, Port: 4848}
		dialTcpAddr = &net.TCPAddr{IP: net.IPv6loopback, Port: 4747}
		dialUdpAddr = &net.UDPAddr{IP: net.IPv6loopback, Port: 4747}
	)
	testcases := []struct {
		t pexEventType
		c *PeerConn
		e pexEvent
	}{
		{
			pexAdd,
			&PeerConn{Peer: Peer{RemoteAddr: udpAddr, Network: udpAddr.Network()}},
			pexEvent{pexAdd, udpAddr.AddrPort(), pp.PexSupportsUtp, nil},
		},
		{
			pexDrop,
			&PeerConn{
				Peer:           Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network(), outgoing: true},
				PeerListenPort: dialTcpAddr.Port,
			},
			pexEvent{pexDrop, tcpAddr.AddrPort(), pp.PexOutgoingConn, nil},
		},
		{
			pexAdd,
			&PeerConn{
				Peer:           Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network()},
				PeerListenPort: dialTcpAddr.Port,
			},
			pexEvent{pexAdd, dialTcpAddr.AddrPort(), 0, nil},
		},
		{
			pexDrop,
			&PeerConn{
				Peer:           Peer{RemoteAddr: udpAddr, Network: udpAddr.Network()},
				PeerListenPort: dialUdpAddr.Port,
			},
			pexEvent{pexDrop, dialUdpAddr.AddrPort(), pp.PexSupportsUtp, nil},
		},
	}
	for i, tc := range testcases {
		c.Run(fmt.Sprintf("%v", i), func(c *qt.C) {
			e, err := tc.c.pexEvent(tc.t)
			c.Assert(err, qt.IsNil)
			c.Check(e, qt.Equals, tc.e)
		})
	}
}

func TestHaveAllThenBitfield(t *testing.T) {
	c := qt.New(t)
	cl := newTestingClient(t)
	tt := cl.newTorrentForTesting()
	// cl.newConnection()
	pc := PeerConn{
		Peer: Peer{t: tt},
	}
	pc.initRequestState()
	pc.legacyPeerImpl = &pc
	tt.conns[&pc] = struct{}{}
	c.Assert(pc.onPeerSentHaveAll(), qt.IsNil)
	c.Check(pc.t.connsWithAllPieces, qt.DeepEquals, map[*Peer]struct{}{&pc.Peer: {}})
	pc.peerSentBitfield([]bool{false, false, true, false, true, true, false, false})
	c.Check(pc.peerMinPieces, qt.Equals, 6)
	c.Check(pc.t.connsWithAllPieces, qt.HasLen, 0)
	c.Assert(pc.t.setInfo(&metainfo.Info{
		PieceLength: 0,
		Pieces:      make([]byte, pieceHash.Size()*7),
	}), qt.IsNil)
	pc.t.onSetInfo()
	c.Check(tt.numPieces(), qt.Equals, 7)
	c.Check(tt.pieceAvailabilityRuns(), qt.DeepEquals, []pieceAvailabilityRun{
		// The last element of the bitfield is irrelevant, as the Torrent actually only has 7
		// pieces.
		{2, 0}, {1, 1}, {1, 0}, {2, 1}, {1, 0},
	})
}

func TestApplyRequestStateWriteBufferConstraints(t *testing.T) {
	c := qt.New(t)
	c.Check(interestedMsgLen, qt.Equals, 5)
	c.Check(requestMsgLen, qt.Equals, 17)
	c.Check(maxLocalToRemoteRequests >= 8, qt.IsTrue)
	c.Logf("max local to remote requests: %v", maxLocalToRemoteRequests)
}

func peerConnForPreferredNetworkDirection(
	localPeerId, remotePeerId int,
	outgoing, utp, ipv6 bool,
) *PeerConn {
	pc := PeerConn{}
	pc.outgoing = outgoing
	if utp {
		pc.Network = "udp"
	}
	if ipv6 {
		pc.RemoteAddr = &net.TCPAddr{IP: net.ParseIP("::420")}
	} else {
		pc.RemoteAddr = &net.TCPAddr{IP: net.IPv4(1, 2, 3, 4)}
	}
	binary.BigEndian.PutUint64(pc.PeerID[:], uint64(remotePeerId))
	cl := Client{}
	binary.BigEndian.PutUint64(cl.peerID[:], uint64(localPeerId))
	pc.t = &Torrent{cl: &cl}
	return &pc
}

func TestPreferredNetworkDirection(t *testing.T) {
	pc := peerConnForPreferredNetworkDirection
	c := qt.New(t)

	// Prefer outgoing to lower peer ID

	c.Check(
		pc(1, 2, true, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, false)),
		qt.IsFalse,
	)
	c.Check(
		pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, true, false, false)),
		qt.IsTrue,
	)
	c.Check(
		pc(2, 1, false, false, false).hasPreferredNetworkOver(pc(2, 1, true, false, false)),
		qt.IsFalse,
	)

	// Don't prefer uTP
	c.Check(
		pc(1, 2, false, true, false).hasPreferredNetworkOver(pc(1, 2, false, false, false)),
		qt.IsFalse,
	)
	// Prefer IPv6
	c.Check(
		pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, true)),
		qt.IsFalse,
	)
	// No difference
	c.Check(
		pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, false)),
		qt.IsFalse,
	)
}

func TestReceiveLargeRequest(t *testing.T) {
	c := qt.New(t)
	cl := newTestingClient(t)
	pc := cl.newConnection(nil, newConnectionOpts{network: "test"})
	tor := cl.newTorrentForTesting()
	tor.info = &metainfo.Info{PieceLength: 3 << 20}
	pc.setTorrent(tor)
	tor._completedPieces.Add(0)
	pc.PeerExtensionBytes.SetBit(pp.ExtensionBitFast, true)
	pc.choking = false
	pc.initMessageWriter()
	req := Request{}
	req.Length = defaultChunkSize
	c.Assert(pc.fastEnabled(), qt.IsTrue)
	c.Check(pc.onReadRequest(req, false), qt.IsNil)
	c.Check(pc.peerRequests, qt.HasLen, 1)
	req.Length = 2 << 20
	c.Check(pc.onReadRequest(req, false), qt.IsNil)
	c.Check(pc.peerRequests, qt.HasLen, 2)
	pc.peerRequests = nil
	pc.t.cl.config.UploadRateLimiter = rate.NewLimiter(1, defaultChunkSize)
	req.Length = defaultChunkSize
	c.Check(pc.onReadRequest(req, false), qt.IsNil)
	c.Check(pc.peerRequests, qt.HasLen, 1)
	req.Length = 2 << 20
	c.Check(pc.onReadRequest(req, false), qt.IsNil)
	c.Check(pc.messageWriter.writeBuffer.Len(), qt.Equals, 17)
}

func TestChunkOverflowsPiece(t *testing.T) {
	c := qt.New(t)
	check := func(begin, length, limit pp.Integer, expected bool) {
		c.Check(chunkOverflowsPiece(ChunkSpec{begin, length}, limit), qt.Equals, expected)
	}
	check(2, 3, 1, true)
	check(2, pp.IntegerMax, 1, true)
	check(2, pp.IntegerMax, 3, true)
	check(2, pp.IntegerMax, pp.IntegerMax, true)
	check(2, pp.IntegerMax-2, pp.IntegerMax, false)
}
