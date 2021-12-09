package torrent

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/anacrolix/missinggo/pubsub"
	"github.com/frankban/quicktest"
	"github.com/stretchr/testify/require"

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
	c := cl.newConnection(nil, false, nil, "io.Pipe", "")
	c.setTorrent(cl.newTorrent(metainfo.Hash{}, nil))
	if err := c.t.setInfo(&metainfo.Info{Pieces: make([]byte, metainfo.HashSize*3)}); err != nil {
		t.Log(err)
	}
	r, w := io.Pipe()
	// c.r = r
	c.w = w
	c.startWriter()
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
	writeSem sync.Mutex
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
	me.writeSem.Unlock()
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
	t := &Torrent{
		cl:                &cl,
		storage:           &storage.Torrent{TorrentImpl: storage.TorrentImpl{Piece: ts.Piece, Close: ts.Close}},
		pieceStateChanges: pubsub.NewPubSub(),
	}
	t.setChunkSize(defaultChunkSize)
	require.NoError(b, t.setInfo(&metainfo.Info{
		Pieces:      make([]byte, 20),
		Length:      1 << 20,
		PieceLength: 1 << 20,
	}))
	t._pendingPieces.Add(0)
	r, w := net.Pipe()
	cn := cl.newConnection(r, true, r.RemoteAddr(), r.RemoteAddr().Network(), regularNetConnPeerConnConnString(r))
	cn.setTorrent(t)
	mrlErrChan := make(chan error)
	msg := pp.Message{
		Type:  pp.Piece,
		Piece: make([]byte, defaultChunkSize),
	}
	go func() {
		cl.lock()
		err := cn.mainReadLoop()
		if err != nil {
			mrlErrChan <- err
		}
		close(mrlErrChan)
	}()
	wb := msg.MustMarshalBinary()
	b.SetBytes(int64(len(msg.Piece)))
	go func() {
		ts.writeSem.Lock()
		for i := 0; i < b.N; i += 1 {
			cl.lock()
			// The chunk must be written to storage everytime, to ensure the
			// writeSem is unlocked.
			t.pendAllChunkSpecs(0)
			cn.validReceiveChunks = map[RequestIndex]int{
				t.requestIndexFromRequest(newRequestFromMessage(&msg)): 1,
			}
			cl.unlock()
			n, err := w.Write(wb)
			require.NoError(b, err)
			require.EqualValues(b, len(wb), n)
			ts.writeSem.Lock()
		}
		if err := w.Close(); err != nil {
			panic(err)
		}
	}()
	mrlErr := <-mrlErrChan
	if mrlErr != nil && !errors.Is(mrlErr, io.EOF) {
		c.Fatal(mrlErr)
	}
	c.Assert(cn._stats.ChunksReadUseful.Int64(), quicktest.Equals, int64(b.N))
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
			pexEvent{pexAdd, udpAddr, pp.PexSupportsUtp, nil},
		},
		{
			pexDrop,
			&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network(), outgoing: true, PeerListenPort: dialTcpAddr.Port}},
			pexEvent{pexDrop, tcpAddr, pp.PexOutgoingConn, nil},
		},
		{
			pexAdd,
			&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, Network: tcpAddr.Network(), PeerListenPort: dialTcpAddr.Port}},
			pexEvent{pexAdd, dialTcpAddr, 0, nil},
		},
		{
			pexDrop,
			&PeerConn{Peer: Peer{RemoteAddr: udpAddr, Network: udpAddr.Network(), PeerListenPort: dialUdpAddr.Port}},
			pexEvent{pexDrop, dialUdpAddr, pp.PexSupportsUtp, nil},
		},
	}
	for i, tc := range testcases {
		e := tc.c.pexEvent(tc.t)
		require.EqualValues(t, tc.e, e, i)
	}
}
