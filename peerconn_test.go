package torrent

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Ensure that no race exists between sending a bitfield, and a subsequent
// Have that would potentially alter it.
func TestSendBitfieldThenHave(t *testing.T) {
	cl := newTestingClient(t)
	c := cl.newConnection(nil, newConnectionOpts{network: "io.Pipe"})
	c.setTorrent(cl.newTorrentForTesting())
	// I think code to handle zero size, no-name torrents is missing. It should be fine to a point.
	err := c.t.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, metainfo.HashSize*3),
		Name:        "dummy",
		PieceLength: 1,
		Length:      3,
	})
	qt.Assert(t, qt.IsNil(err))
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
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(n, 15))
	// Here we see that the bitfield doesn't have piece 2 set, as that should
	// arrive in the following Have message.
	qt.Assert(t, qt.Equals(string(b), "\x00\x00\x00\x02\x05@\x00\x00\x00\x05\x04\x00\x00\x00\x02"))
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
	return 0, errors.New("not implemented")
}

func (me *torrentStorage) WriteAt(b []byte, _ int64) (int, error) {
	if len(b) != defaultChunkSize {
		panic(len(b))
	}
	me.allChunksWritten.Done()
	return len(b), nil
}

type torrentStorageClient struct {
	ts *torrentStorage
}

func (t torrentStorageClient) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	ts := t.ts
	return storage.TorrentImpl{Piece: ts.Piece, Close: ts.Close}, nil
}

func BenchmarkConnectionMainReadLoop(b *testing.B) {
	var cl Client
	cfg := TestingConfig(b)
	ts := &torrentStorage{}
	cfg.DefaultStorage = &torrentStorageClient{ts}
	cl.init(cfg)
	t, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:                 testingTorrentInfoHash,
		Storage:                  &torrentStorageClient{ts},
		DisableInitialPieceCheck: true,
	})
	qt.Assert(b, qt.IsNil(t.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, 20),
		Length:      1 << 20,
		PieceLength: 1 << 20,
	})))
	//t.storage = &storage.Torrent{TorrentImpl: storage.TorrentImpl{Piece: ts.Piece, Close: ts.Close}}
	//t.onSetInfo()
	cl.lock()
	t.updatePiecePriority(0, "benchmark setup")
	cl.unlock()
	r, w := net.Pipe()
	b.Logf("pipe reader remote addr: %v", r.RemoteAddr())
	cn := cl.newConnection(r, newConnectionOpts{
		outgoing: true,
		// TODO: This is a hack to give the pipe a bannable remote address.
		remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, 4}), 1234),
		network:    r.RemoteAddr().Network(),
		connString: regularNetConnPeerConnConnString(r),
	})
	qt.Assert(b, qt.IsTrue(cn.bannableAddr.Ok))
	cn.setTorrent(t)
	requestIndexBegin := t.pieceRequestIndexBegin(0)
	requestIndexEnd := t.pieceRequestIndexBegin(1)
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
				qt.Assert(b, qt.IsNil(err))
				qt.Assert(b, qt.Equals(n, len(wb)))
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
	qt.Assert(b, qt.IsNil(err))
	qt.Assert(b, qt.Equals(cn._stats.ChunksReadUseful.Int64(), int64(b.N)*int64(numRequests)))
	qt.Assert(b, qt.IsTrue(t.smartBanCache.HasBlocks()))
}

func TestPeerConnRejectsUnsolicitedHashes(t *testing.T) {
	var knownRoot infohash_v2.T
	knownRoot[0] = 1
	cl := &Client{}
	cl._mu.client = cl
	cl.slogger = slog.Default()
	tor := &Torrent{
		cl:        cl,
		chunkSize: defaultChunkSize,
		files: &[]*File{{
			piecesRoot: g.Some(knownRoot),
		}},
	}
	var msgBuf bytes.Buffer
	// Feed the message through the read loop so this covers the network-reachable path, not just the
	// private handler.
	msg := pp.Message{
		Type:   pp.Hashes,
		Length: 1,
		Hashes: [][32]byte{{}},
	}
	qt.Assert(t, qt.IsNil(msg.WriteTo(&msgBuf)))
	cn := &PeerConn{
		Peer: Peer{
			cl:        cl,
			t:         tor,
			callbacks: &Callbacks{},
		},
		r: bytes.NewReader(msgBuf.Bytes()),
	}

	cl.lock()
	err := cn.mainReadLoop()
	cl.unlock()
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "unsolicited hashes message"))
}

func TestPeerConnAcceptsSolicitedHashes(t *testing.T) {
	pieceHashes := [][32]byte{{}, {}}
	piecesRoot := infohash_v2.T(merkle.Root(pieceHashes))
	cl := &Client{}
	tor := &Torrent{
		cl:        cl,
		chunkSize: defaultChunkSize,
		info: &metainfo.Info{
			PieceLength: 1,
			// Two zero v1 piece hashes; the per-piece hash is derived from here now.
			Pieces: make([]byte, 2*metainfo.HashSize),
		},
		pieces: make([]pieceState, 2),
		files: &[]*File{{
			t:          nil,
			length:     2,
			fi:         metainfo.FileInfo{Length: 2, PiecesRoot: g.Some(piecesRoot)},
			piecesRoot: g.Some(piecesRoot),
		}},
	}
	(*tor.files)[0].t = tor
	msg := pp.Message{
		Type:       pp.Hashes,
		PiecesRoot: piecesRoot,
		Length:     2,
		Hashes:     pieceHashes,
	}
	cn := &PeerConn{
		Peer: Peer{
			cl: cl,
			t:  tor,
		},
		sentHashRequests: map[hashRequest]struct{}{
			hashRequestFromMessage(msg): {},
		},
	}

	qt.Assert(t, qt.IsNil(cn.onReadHashes(&msg)))
	_, sentHashRequestPresent := cn.sentHashRequests[hashRequestFromMessage(msg)]
	qt.Assert(t, qt.IsFalse(sentHashRequestPresent))
	// Matching the requested root should promote the received file layer hashes into piece v2 hashes.
	qt.Assert(t, qt.DeepEquals(tor.pieces[0].hashV2, &pieceHashes[0]))
	qt.Assert(t, qt.DeepEquals(tor.pieces[1].hashV2, &pieceHashes[1]))
}

func TestPeerConnRejectsHashesForMissingRoot(t *testing.T) {
	var knownRoot infohash_v2.T
	knownRoot[0] = 1
	var missingRoot infohash_v2.T
	missingRoot[0] = 2
	cl := &Client{}
	tor := &Torrent{
		cl:        cl,
		chunkSize: defaultChunkSize,
		files: &[]*File{{
			piecesRoot: g.Some(knownRoot),
		}},
	}
	msg := pp.Message{
		Type:       pp.Hashes,
		PiecesRoot: missingRoot,
		Length:     1,
		Hashes:     [][32]byte{{}},
	}
	cn := &PeerConn{
		Peer: Peer{
			cl: cl,
			t:  tor,
		},
		sentHashRequests: map[hashRequest]struct{}{
			hashRequestFromMessage(msg): {},
		},
	}

	err := cn.onReadHashes(&msg)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "no file for pieces root"))
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
		qt.Assert(t, qt.Equals(f, tc.f), qt.Commentf("%v", i))
	}
}

func TestConnPexEvent(t *testing.T) {
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
		t.Run(fmt.Sprintf("%v", i), func(t *testing.T) {
			e, err := tc.c.pexEvent(tc.t)
			qt.Assert(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(e, tc.e))
		})
	}
}

func TestHaveAllThenBitfield(t *testing.T) {
	cl := newTestingClient(t)
	tt := cl.newTorrentForTesting()
	//pc := cl.newConnection(nil, newConnectionOpts{})
	pc := PeerConn{
		Peer: Peer{t: tt},
	}
	pc.initRequestState()
	pc.legacyPeerImpl = &pc
	tt.conns[&pc] = struct{}{}
	g.InitNew(&pc.callbacks)
	qt.Assert(t, qt.IsNil(pc.onPeerSentHaveAll()))
	qt.Check(t, qt.DeepEquals(pc.t.connsWithAllPieces, map[*Peer]struct{}{&pc.Peer: {}}))
	pc.peerSentBitfield([]bool{false, false, true, false, true, true, false, false})
	qt.Check(t, qt.Equals(pc.peerMinPieces, 6))
	qt.Check(t, qt.HasLen(pc.t.connsWithAllPieces, 0))
	qt.Assert(t, qt.IsNil(pc.t.setInfoUnlocked(&metainfo.Info{
		Name:        "herp",
		Length:      7,
		PieceLength: 1,
		Pieces:      make([]byte, pieceHash.Size()*7),
	})))
	qt.Check(t, qt.Equals(tt.numPieces(), 7))
	qt.Check(t, qt.DeepEquals(tt.pieceAvailabilityRuns(), []pieceAvailabilityRun{
		// The last element of the bitfield is irrelevant, as the Torrent actually only has 7
		// pieces.
		{2, 0}, {1, 1}, {1, 0}, {2, 1}, {1, 0},
	}))
}

func TestApplyRequestStateWriteBufferConstraints(t *testing.T) {
	qt.Check(t, qt.Equals(interestedMsgLen, 5))
	qt.Check(t, qt.Equals(requestMsgLen, 17))
	qt.Check(t, qt.IsTrue(maxLocalToRemoteRequests >= 8))
	t.Logf("max local to remote requests: %v", maxLocalToRemoteRequests)
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

	// Prefer outgoing to lower peer ID

	qt.Check(t,
		qt.IsFalse(pc(1, 2, true, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, false))),
	)
	qt.Check(t,
		qt.IsTrue(pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, true, false, false))),
	)
	qt.Check(t,
		qt.IsFalse(pc(2, 1, false, false, false).hasPreferredNetworkOver(pc(2, 1, true, false, false))),
	)

	// Don't prefer uTP
	qt.Check(t,
		qt.IsFalse(pc(1, 2, false, true, false).hasPreferredNetworkOver(pc(1, 2, false, false, false))),
	)
	// Prefer IPv6
	qt.Check(t,
		qt.IsFalse(pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, true))),
	)
	// No difference
	qt.Check(t,
		qt.IsFalse(pc(1, 2, false, false, false).hasPreferredNetworkOver(pc(1, 2, false, false, false))),
	)
}

func TestReceiveLargeRequest(t *testing.T) {
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
	qt.Assert(t, qt.IsTrue(pc.fastEnabled()))
	qt.Check(t, qt.IsNil(pc.onReadRequest(req, false)))
	qt.Check(t, qt.HasLen(pc.unreadPeerRequests, 1))
	req.Length = 2 << 20
	qt.Check(t, qt.IsNil(pc.onReadRequest(req, false)))
	qt.Check(t, qt.HasLen(pc.unreadPeerRequests, 2))
	pc.unreadPeerRequests = nil
	pc.t.cl.config.UploadRateLimiter = rate.NewLimiter(1, defaultChunkSize)
	req.Length = defaultChunkSize
	qt.Check(t, qt.IsNil(pc.onReadRequest(req, false)))
	qt.Check(t, qt.HasLen(pc.unreadPeerRequests, 1))
	req.Length = 2 << 20
	qt.Check(t, qt.IsNil(pc.onReadRequest(req, false)))
	qt.Check(t, qt.Equals(pc.messageWriter.writeBuffer.Len(), 17))
}

func TestChunkOverflowsPiece(t *testing.T) {
	check := func(begin, length, limit pp.Integer, expected bool) {
		qt.Check(t, qt.Equals(chunkOverflowsPiece(ChunkSpec{begin, length}, limit), expected))
	}
	check(2, 3, 1, true)
	check(2, pp.IntegerMax, 1, true)
	check(2, pp.IntegerMax, 3, true)
	check(2, pp.IntegerMax, pp.IntegerMax, true)
	check(2, pp.IntegerMax-2, pp.IntegerMax, false)
}

// TestServePeerRequestTorrentClosedStorageReadFails verifies that servePeerRequest does not panic
// when a storage read fails after the torrent has been closed. Previously, peerRequestDataReadFailed
// returned early on a closed torrent without removing the request from unreadPeerRequests,
// violating the invariant checked by the deferred assertion in servePeerRequest.
func TestServePeerRequestTorrentClosedStorageReadFails(t *testing.T) {
	var cl Client
	cfg := TestingConfig(t)
	ts := &torrentStorage{}
	cfg.DefaultStorage = &torrentStorageClient{ts}
	cl.init(cfg)
	t.Cleanup(func() { cl.Close() })

	tor, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:                 testingTorrentInfoHash,
		Storage:                  &torrentStorageClient{ts},
		DisableInitialPieceCheck: true,
	})
	qt.Assert(t, qt.IsNil(tor.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, metainfo.HashSize),
		PieceLength: 1,
		Length:      1,
		Name:        "test",
	})))

	pc := cl.newConnection(nil, newConnectionOpts{network: "test"})
	pc.setTorrent(tor)

	req := Request{Index: 0, ChunkSpec: ChunkSpec{Begin: 0, Length: 1}}
	g.MakeMapIfNil(&pc.unreadPeerRequests)
	pc.unreadPeerRequests[req] = struct{}{}

	// Simulate the torrent being dropped while the connection is still active.
	tor.Drop()

	cl.lock()
	defer cl.unlock()
	pc.servePeerRequest(req)

	qt.Check(t, qt.IsFalse(g.MapContains(pc.unreadPeerRequests, req)))
}
