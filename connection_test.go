package torrent

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding"
	"hash"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/utp"
	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/cryptox"
	"github.com/james-lawrence/torrent/internal/md5x"
	"github.com/james-lawrence/torrent/internal/netx"
	"github.com/james-lawrence/torrent/internal/testx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/james-lawrence/torrent/torrenttest"
)

// Ensure that no race exists between sending a bitfield, and a subsequent
// Have that would potentially alter it.
func TestSendBitfieldThenHave(t *testing.T) {
	// t.SkipNow()
	cl := &Client{
		config: TestingConfig(t),
	}
	ts, err := New(metainfo.Hash{})
	require.NoError(t, err)
	tt := newTorrent(cl, ts)
	require.NoError(t, err)
	tt.setInfo(&metainfo.Info{
		Pieces:      make([]byte, metainfo.HashSize*3),
		Length:      24 * (1 << 10),
		PieceLength: 8 * (1 << 10),
	})
	c := cl.newConnection(nil, false, netip.AddrPort{})
	c.setTorrent(tt)

	r, w := io.Pipe()
	c.r = r
	c.w = w
	go connwriterinit(t.Context(), c, time.Minute)

	c.t.chunks.completed.Add(1)
	c.PostBitfield( /*[]bool{false, true, false}*/ )
	c.Have(2)
	b := make([]byte, 15)
	n, err := io.ReadFull(r, b)
	// This will cause connection.writer to terminate.
	c.closed.Store(true)
	require.NoError(t, err)
	require.EqualValues(t, 15, n)
	// Here we see that the bitfield doesn't have piece 2 set, as that should
	// arrive in the following Have message.
	require.EqualValues(t, "\x00\x00\x00\x02\x05@\x00\x00\x00\x05\x04\x00\x00\x00\x02", string(b))
}

func TestProtocolSequencesDownloading(t *testing.T) {
	const iolimit int64 = 128 * bytesx.KiB

	genconnection := func(t *testing.T, seed string, n uint64, pbits, sbits pp.ExtensionBits) (p *connection, s *connection, _ hash.Hash, _ Metadata) {
		var (
			__pconn chan net.Conn = make(chan net.Conn, 1)
			_perr   error
		)

		l, err := utp.Listen(":0")
		require.NoError(t, err)
		cfgs := TestingConfig(t, ClientConfigSeed(true))
		sclient, err := NewClient(cfgs)
		require.NoError(t, err)

		cfgl := TestingConfig(t)
		pclient, err := NewClient(cfgl)
		require.NoError(t, err)
		info, _md5, err := torrenttest.Seeded(t.TempDir(), n, cryptox.NewChaCha8(seed))
		require.NoError(t, err)
		meta, err := NewFromInfo(info)
		require.NoError(t, err)

		go func() {
			var (
				___pconn net.Conn
			)
			___pconn, _perr = utp.DialContext(t.Context(), l.Addr().String())
			__pconn <- ___pconn
		}()

		c, err := l.Accept()
		require.NoError(t, err)
		_pconn := <-__pconn
		require.NoError(t, _perr)
		require.NotNil(t, _pconn)

		snetip := testx.Must(netx.AddrPort(_pconn.RemoteAddr()))(t)
		pnetip := testx.Must(netx.AddrPort(_pconn.LocalAddr()))(t)

		pconn := newConnection(cfgl, _pconn, true, snetip, &pbits, pnetip.Port(), 0)
		pconn.PeerExtensionBytes = sbits
		pconn.PeerID = int160.Random()
		pconn.completedHandshake = time.Now()
		pconn.t = newTorrent(pclient, meta)
		pconn.t.chunks.fill(pconn.t.chunks.missing)

		sconn := newConnection(cfgs, c, false, pnetip, &sbits, snetip.Port(), 0)
		sconn.PeerExtensionBytes = pbits
		sconn.PeerID = int160.Random()
		sconn.completedHandshake = time.Now()
		sconn.t = newTorrent(sclient, meta)
		sconn.t.chunks.fill(sconn.t.chunks.completed)
		require.True(t, sconn.t.seeding(), "seeding should be enabled")

		return pconn, sconn, _md5, meta
	}

	t.Run("plaintext vanilla sequence", func(t *testing.T) {
		pconn, sconn, expected, meta := genconnection(
			t,
			t.Name(),
			uint64(iolimit),
			pp.NewExtensionBits(pp.ExtensionBitExtended),
			pp.NewExtensionBits(pp.ExtensionBitExtended),
		)
		_ = meta

		require.NotNil(t, pconn)
		require.NotNil(t, sconn)
		n, err := pp.Write(pconn.writeBuffer)
		require.NoError(t, err)
		require.Equal(t, 0, n)

		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		go func() {
			pt := pconn.t
			pconn.t = nil
			cancel(RunHandshookConn(pconn, pt))
		}()

		d := pp.NewDecoder(sconn.conn, sconn.t.chunkPool)
		deliver := func(dst *connection, msg ...encoding.BinaryMarshaler) (int, error) {
			n1, err := pp.Write(dst.writeBuffer, msg...)
			if err != nil {
				return n1, err
			}
			n2, err := dst.Flush()
			require.Equal(t, n1, n2)
			return n2, err
		}

		// after sending bit field should receive:
		// extend payload.
		msg, err := sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Extended, msg.Type)
		require.Equal(t, 138, len(msg.ExtendedPayload))

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Bitfield, msg.Type)

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Port, msg.Type)

		require.NoError(t, ConnExtensions(ctx, sconn))
		require.Equal(t, 0, sconn.writeBuffer.Len())

		n, err = deliver(sconn, pp.NewInterested(false), pp.NewUnchoked())
		require.NoError(t, err)
		require.Equal(t, 10, n)

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Extended, msg.Type)

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Unchoke, msg.Type)

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Interested, msg.Type)

		var (
			buf      bytes.Buffer
			regenned = md5.New()
		)

		n0, err := io.Copy(io.MultiWriter(&buf, regenned), io.LimitReader(cryptox.NewChaCha8(t.Name()), iolimit))
		require.NoError(t, err)
		require.Equal(t, iolimit, n0)
		require.Equal(t, md5x.FormatHex(expected), md5x.FormatHex(regenned))
		c := bytes.NewReader(buf.Bytes())
		for range 8 {
			msg, err = sconn.ReadOne(ctx, d)
			require.NoError(t, err)
			torrenttest.RequireMessageType(t, pp.Request, msg.Type)

			p := sconn.t.piece(msg.Index.Int())
			chunk, err := io.ReadAll(io.NewSectionReader(c, p.Offset()+int64(msg.Begin), int64(msg.Length)))
			require.NoError(t, err)

			_, err = deliver(sconn, pp.NewPiece(msg.Index, msg.Begin, chunk))
			require.NoError(t, err)
			require.Equal(t, msg.Length.Int(), len(chunk)) // message overhead
		}

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Choke, msg.Type)

		pconn.Close()

		_, err = sconn.ReadOne(ctx, d)
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("plaintext fastex enabled sequence", func(t *testing.T) {
		pconn, sconn, expected, meta := genconnection(
			t,
			t.Name(),
			uint64(iolimit),
			pp.NewExtensionBits(pp.ExtensionBitExtended, pp.ExtensionBitFast),
			pp.NewExtensionBits(pp.ExtensionBitExtended, pp.ExtensionBitFast),
		)
		_ = meta
		require.Equal(t, md5x.FormatHex(expected), "d02c34adaba8570757dcd8efa9333cfe")
		require.NotNil(t, pconn)
		require.NotNil(t, sconn)
		n, err := pp.Write(pconn.writeBuffer)
		require.NoError(t, err)
		require.Equal(t, 0, n)

		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		go func() {
			pt := pconn.t
			pconn.t = nil
			cancel(RunHandshookConn(pconn, pt))
		}()

		d := pp.NewDecoder(sconn.conn, sconn.t.chunkPool)
		deliver := func(dst *connection, msg ...encoding.BinaryMarshaler) (int, error) {

			pending := dst.writeBuffer.Len()
			dst.cmu().Lock()
			n1, err := pp.Write(dst.writeBuffer, msg...)
			dst.cmu().Unlock()

			if err != nil {
				return n1, err
			}
			n2, err := dst.Flush()
			require.Equal(t, pending+n1, n2, "unexpected misalignment for write and flush pending(%d) + write(%d) != flush(%d)", pending, n1, n2)
			return n2, err
		}

		// after sending bit field should receive:
		// extend payload.
		msg, err := sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Extended, msg.Type)
		require.Equal(t, 138, len(msg.ExtendedPayload))

		// --------------------------------------- allow fast extension ----------------------------------------------
		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.HaveNone, msg.Type)
		// --------------------------------------- allow fast extension ----------------------------------------------

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Port, msg.Type)
		// --------------------------------------- allow vanilla messages completed ----------------------------------

		require.NoError(t, ConnExtensions(ctx, sconn))
		require.Equal(t, 0, sconn.writeBuffer.Len())

		require.Equal(t, []uint32{0}, sconn.peerfastset.ToArray())
		n, err = deliver(sconn, pp.NewInterested(false), pp.NewAllowedFast(0))
		require.NoError(t, err)
		require.Equal(t, 14, n)

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Extended, msg.Type)

		var (
			buf      bytes.Buffer
			regenned = md5.New()
		)

		n0, err := io.Copy(io.MultiWriter(&buf, regenned), io.LimitReader(cryptox.NewChaCha8(t.Name()), iolimit))
		require.NoError(t, err)
		require.Equal(t, iolimit, n0)
		require.Equal(t, md5x.FormatHex(expected), md5x.FormatHex(regenned))
		c := bytes.NewReader(buf.Bytes())
		for i := 0; i < 8; {
			msg, err = sconn.ReadOne(ctx, d)
			require.NoError(t, err)
			if msg.Type != pp.Request {
				continue
			} else {
				i++
			}
			torrenttest.RequireMessageType(t, pp.Request, msg.Type)

			p := sconn.t.piece(msg.Index.Int())
			chunk, err := io.ReadAll(io.NewSectionReader(c, p.Offset()+int64(msg.Begin), int64(msg.Length.Int64())))
			require.NoError(t, err)

			_, err = deliver(sconn, pp.NewPiece(msg.Index, msg.Begin, chunk))
			require.NoError(t, err)
			require.Equal(t, msg.Length.Int(), len(chunk)) // message overhead
		}

		msg, err = sconn.ReadOne(ctx, d)
		require.NoError(t, err)
		torrenttest.RequireMessageType(t, pp.Choke, msg.Type)

		pconn.Close()

		_, err = sconn.ReadOne(ctx, d)
		require.ErrorIs(t, err, io.EOF)
	})
}

type torrentStorage struct {
	writeSem sync.Mutex
}

func (me *torrentStorage) Close() error { return nil }

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
	cl := &Client{
		config: &ClientConfig{
			DownloadRateLimiter: rate.NewLimiter(rate.Inf, 0),
		},
	}

	ts := &torrentStorage{}
	t := newTorrent(cl, Metadata{ChunkSize: defaultChunkSize})
	t.storage = ts
	require.NoError(b, t.setInfo(&metainfo.Info{
		Pieces:      make([]byte, 20),
		Length:      1 << 20,
		PieceLength: 1 << 20,
	}))
	t.setChunkSize(defaultChunkSize)
	// t.makePieces()
	t.chunks.ChunksPend(0)
	r, w := net.Pipe()
	cn := cl.newConnection(r, true, netip.AddrPort{})
	cn.setTorrent(t)
	mrlErr := make(chan error)
	msg := pp.Message{
		Type:  pp.Piece,
		Piece: make([]byte, defaultChunkSize),
	}
	go func() {
		cl.lock()
		err := cn.mainReadLoop(b.Context())
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
			// TODO
			// t.pieces[0].dirtyChunks.Clear()
			cl.unlock()
			n, err := w.Write(wb)
			require.NoError(b, err)
			require.EqualValues(b, len(wb), n)
			ts.writeSem.Lock()
		}
	}()
	require.NoError(b, <-mrlErr)
	require.EqualValues(b, b.N, cn.stats.ChunksReadUseful.Int64())
}

func TestPexPeerFlags(t *testing.T) {
	var testcases = []struct {
		conn *connection
		f    pp.PexPeerFlags
	}{
		{&connection{outgoing: false, PeerPrefersEncryption: false}, 0},
		{&connection{outgoing: false, PeerPrefersEncryption: true}, pp.PexPrefersEncryption},
		{&connection{outgoing: true, PeerPrefersEncryption: false}, pp.PexOutgoingConn},
		{&connection{outgoing: true, PeerPrefersEncryption: true}, pp.PexOutgoingConn | pp.PexPrefersEncryption},
	}
	for i, tc := range testcases {
		f := tc.conn.pexPeerFlags()
		require.EqualValues(t, tc.f, f, i)
	}
}
