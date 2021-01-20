package torrent

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/missinggo/pubsub"
	"github.com/bradfitz/iter"
	"github.com/frankban/quicktest"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

// Ensure that no race exists between sending a bitfield, and a subsequent
// Have that would potentially alter it.
func TestSendBitfieldThenHave(t *testing.T) {
	cl := Client{
		config: TestingConfig(),
	}
	cl.initLogger()
	c := cl.newConnection(nil, false, nil, "", "")
	c.setTorrent(cl.newTorrent(metainfo.Hash{}, nil))
	c.t.setInfo(&metainfo.Info{
		Pieces: make([]byte, metainfo.HashSize*3),
	})
	r, w := io.Pipe()
	c.r = r
	c.w = w
	go c.writer(time.Minute)
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
	cl := &Client{
		config: &ClientConfig{
			DownloadRateLimiter: unlimited,
		},
	}
	cl.initLogger()
	ts := &torrentStorage{}
	t := &Torrent{
		cl:                cl,
		storage:           &storage.Torrent{TorrentImpl: ts},
		pieceStateChanges: pubsub.NewPubSub(),
	}
	require.NoError(b, t.setInfo(&metainfo.Info{
		Pieces:      make([]byte, 20),
		Length:      1 << 20,
		PieceLength: 1 << 20,
	}))
	t.setChunkSize(defaultChunkSize)
	t._pendingPieces.Set(0, PiecePriorityNormal.BitmapPriority())
	r, w := net.Pipe()
	cn := cl.newConnection(r, true, nil, "", "")
	cn.setTorrent(t)
	mrlErr := make(chan error)
	msg := pp.Message{
		Type:  pp.Piece,
		Piece: make([]byte, defaultChunkSize),
	}
	go func() {
		cl.lock()
		err := cn.mainReadLoop()
		if err != nil {
			mrlErr <- err
		}
		close(mrlErr)
	}()
	wb := msg.MustMarshalBinary()
	b.SetBytes(int64(len(msg.Piece)))
	go func() {
		defer w.Close()
		ts.writeSem.Lock()
		for range iter.N(b.N) {
			cl.lock()
			// The chunk must be written to storage everytime, to ensure the
			// writeSem is unlocked.
			t.pieces[0]._dirtyChunks.Clear()
			cn.validReceiveChunks = map[request]int{newRequestFromMessage(&msg): 1}
			cl.unlock()
			n, err := w.Write(wb)
			require.NoError(b, err)
			require.EqualValues(b, len(wb), n)
			ts.writeSem.Lock()
		}
	}()
	c.Assert([]error{nil, io.EOF}, quicktest.Contains, <-mrlErr)
	c.Assert(cn._stats.ChunksReadUseful.Int64(), quicktest.Equals, int64(b.N))
}

func TestConnPexPeerFlags(t *testing.T) {
	var (
		tcpAddr = &net.TCPAddr{IP: net.IPv6loopback, Port: 4848}
		udpAddr = &net.UDPAddr{IP: net.IPv6loopback, Port: 4848}
	)
	var testcases = []struct {
		conn *PeerConn
		f    pp.PexPeerFlags
	}{
		{&PeerConn{Peer: Peer{outgoing: false, PeerPrefersEncryption: false}}, 0},
		{&PeerConn{Peer: Peer{outgoing: false, PeerPrefersEncryption: true}}, pp.PexPrefersEncryption},
		{&PeerConn{Peer: Peer{outgoing: true, PeerPrefersEncryption: false}}, pp.PexOutgoingConn},
		{&PeerConn{Peer: Peer{outgoing: true, PeerPrefersEncryption: true}}, pp.PexOutgoingConn | pp.PexPrefersEncryption},
		{&PeerConn{Peer: Peer{RemoteAddr: udpAddr}}, pp.PexSupportsUtp},
		{&PeerConn{Peer: Peer{RemoteAddr: udpAddr, outgoing: true}}, pp.PexOutgoingConn | pp.PexSupportsUtp},
		{&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, outgoing: true}}, pp.PexOutgoingConn},
		{&PeerConn{Peer: Peer{RemoteAddr: tcpAddr}}, 0},
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
	var testcases = []struct {
		t pexEventType
		c *PeerConn
		e pexEvent
	}{
		{
			pexAdd,
			&PeerConn{Peer: Peer{RemoteAddr: udpAddr}},
			pexEvent{pexAdd, udpAddr, pp.PexSupportsUtp},
		},
		{
			pexDrop,
			&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, outgoing: true, PeerListenPort: dialTcpAddr.Port}},
			pexEvent{pexDrop, tcpAddr, pp.PexOutgoingConn},
		},
		{
			pexAdd,
			&PeerConn{Peer: Peer{RemoteAddr: tcpAddr, PeerListenPort: dialTcpAddr.Port}},
			pexEvent{pexAdd, dialTcpAddr, 0},
		},
		{
			pexDrop,
			&PeerConn{Peer: Peer{RemoteAddr: udpAddr, PeerListenPort: dialUdpAddr.Port}},
			pexEvent{pexDrop, dialUdpAddr, pp.PexSupportsUtp},
		},
	}
	for i, tc := range testcases {
		e := tc.c.pexEvent(tc.t)
		require.EqualValues(t, tc.e, e, i)
	}
}
