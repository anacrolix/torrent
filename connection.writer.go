package torrent

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/bep0006"
	pp "github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/cstate"
	"github.com/james-lawrence/torrent/internal/atomicx"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/langx"
)

func RunHandshookConn(c *connection, t *torrent) error {
	c.setTorrent(t)

	c.conn.SetWriteDeadline(time.Time{})
	c.r = deadlineReader{c.conn, c.r}
	completedHandshakeConnectionFlags.Add(c.connectionFlags(), 1)

	defer t.event.Broadcast()
	defer t.dropConnection(c)

	if err := t.addConnection(c); err != nil {
		return errorsx.Wrap(err, "error adding connection")
	}

	defer c.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	c.cfg.debug().Printf("%p exchanging extensions\n", c)

	go func() {
		if err := ConnExtensions(ctx, c); err != nil {
			errorsx.Log(err)
			cancel(errorsx.Wrap(err, "error sending configuring connection"))
			return
		}

		err := connwriterinit(ctx, c, 10*time.Second)
		errorsx.Log(err)
		cancel(err)
	}()

	if err := c.mainReadLoop(ctx); err != nil {
		cancel(err)
		c.cfg.Handshaker.Release(c.conn, err)
		return errorsx.Wrap(err, "error during main read loop")
	}

	return context.Cause(ctx)
}

// See the order given in Transmission's tr_peerMsgsNew.
func ConnExtensions(ctx context.Context, cn *connection) error {
	log.Println("conn extensions initiated")
	defer log.Println("conn extensions completed")
	return cstate.Run(ctx, connexinit(cn, connexdht(cn, connexfast(cn, connflush(cn, nil)))), cn.cfg.debug())
}

func connflush(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		_, err := cn.Flush()
		if err != nil {
			return cstate.Failure(errorsx.Wrap(err, "failed to send requests"))
		}
		return n
	})
}

func connexinit(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		if !cn.extensions.Supported(cn.PeerExtensionBytes, pp.ExtensionBitExtended) {
			return n
		}

		defer log.Println("extended handshake extension completed")
		// TODO: We can figured the port and address out specific to the socket
		// used.
		msg := pp.ExtendedHandshakeMessage{
			M:            cn.cfg.extensions,
			V:            cn.cfg.ExtendedHandshakeClientVersion,
			Reqq:         cn.cfg.maximumOutstandingRequests,
			YourIp:       pp.CompactIp(cn.remoteAddr.Addr().AsSlice()),
			Encryption:   cn.cfg.HeaderObfuscationPolicy.Preferred || !cn.cfg.HeaderObfuscationPolicy.RequirePreferred,
			Port:         cn.localport,
			MetadataSize: cn.t.metadataSize(),
			Ipv4:         pp.CompactIp(cn.cfg.publicIP4.To4()),
			Ipv6:         cn.cfg.publicIP6.To16(),
		}

		encoded, err := bencode.Marshal(msg)
		if err != nil {
			return cstate.Failure(errorsx.Wrapf(err, "unable to encode message %T", msg))
		}

		_, err = cn.Post(pp.NewExtendedHandshake(encoded))

		if err != nil {
			return cstate.Failure(errorsx.Wrapf(err, "unable to encode message %T", msg))
		}

		return n
	})
}

func connexfast(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		defer cn.cfg.debug().Printf("c(%p) seed(%t) fast extension completed\n", cn, cn.t.seeding())
		if !cn.supported(pp.ExtensionBitFast) {
			if _, err := cn.PostBitfield(); err != nil {
				return cstate.Failure(err)
			}
			return n
		}

		if cn.t.haveInfo() {
			cn.peerfastset = errorsx.Zero(bep0006.AllowedFastSet(cn.remoteAddr.Addr(), cn.t.md.ID, cn.t.chunks.pieces, min(32, cn.t.chunks.pieces)))
		}

		switch readable := cn.t.chunks.Readable(); readable {
		case 0:
			cn.cfg.debug().Printf("c(%p) seed(%t) posting allow fast have none: %d/%d\n", cn, cn.t.seeding(), readable, cn.t.chunks.cmaximum)
			if _, err := cn.Post(pp.NewHaveNone()); err != nil {
				return cstate.Failure(err)
			}
			cn.sentHaves.Clear()
			return n
		case uint64(cn.t.chunks.cmaximum):
			cn.cfg.debug().Printf("c(%p) seed(%t) posting allow fast have all: %d/%d\n", cn, cn.t.seeding(), readable, cn.t.chunks.cmaximum)
			if _, err := cn.Post(pp.NewHaveAll()); err != nil {
				return cstate.Failure(err)
			}

			cn.sentHaves.AddRange(0, cn.t.chunks.pieces)

			for _, v := range cn.peerfastset.ToArray() {
				if _, err := cn.Post(pp.NewAllowedFast(v)); err != nil {
					return cstate.Failure(err)
				}
			}

			return n
		default:
			cn.cfg.debug().Printf("c(%p) seed(%t) posting  bitfield: %d/%d\n", cn, cn.t.seeding(), readable, cn.t.chunks.cmaximum)
			if _, err := cn.PostBitfield(); err != nil {
				return cstate.Failure(err)
			}
		}

		return n
	})
}

func connexdht(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		if !(cn.extensions.Supported(cn.PeerExtensionBytes, pp.ExtensionBitDHT) && cn.dhtport > 0) {
			cn.cfg.debug().Println("posting dht not supported")
			return n
		}

		defer log.Println("dht extension completed")

		// TODO: this seems incorrect. we're posting identically the same message
		// as the localport below.
		_, err := cn.Post(pp.NewPort(uint16(cn.dhtport)))
		if err != nil {
			return cstate.Failure(err)
		}

		return n
	})
}

// Routine that writes to the peer. Some of what to write is buffered by
// activity elsewhere in the Client, and some is determined locally when the
// connection is writable.
func connwriterinit(ctx context.Context, cn *connection, to time.Duration) error {
	cn.cfg.info().Printf("c(%p) writer initiated\n", cn)
	defer cn.cfg.info().Printf("c(%p) writer completed\n", cn)
	defer cn.checkFailures()
	defer cn.deleteAllRequests()

	ts := time.Now()
	ws := &writerstate{
		bufferLimit:      64 * bytesx.KiB,
		connection:       cn,
		keepAliveTimeout: to,
		lastwrite:        ts,
		resync:           atomicx.Pointer(ts.Add(to)),
	}

	return cstate.Run(ctx, connWriterSyncChunks(ws), cn.cfg.debug())
}

type writerstate struct {
	*connection
	bufferLimit      int
	keepAliveTimeout time.Duration
	lastwrite        time.Time
	resync           *atomic.Pointer[time.Time]
}

func (t *writerstate) String() string {
	return fmt.Sprintf("c(%p) seed(%t)", t.connection, t.connection.t.seeding())
}

func connwriterclosed(ws *writerstate, next cstate.T) cstate.T {
	return _connWriterClosed{writerstate: ws, next: next}
}

type _connWriterClosed struct {
	*writerstate
	next cstate.T
}

func (t _connWriterClosed) Update(ctx context.Context, _ *cstate.Shared) (r cstate.T) {
	ws := t.writerstate

	// delete requests that were requested beyond the timeout.
	timedout := func(cn *connection, grace time.Duration) bool {
		ts := time.Now()
		return cn.lastUsefulChunkReceived.Add(grace).Before(ts) && cn.t.chunks.Missing() > 0
	}

	if ws.closed.Load() {
		return nil
	}

	if ts := *ws.lastMessageReceived.Load(); time.Since(ts) > 2*ws.keepAliveTimeout {
		return cstate.Failure(
			errorsx.Timedout(
				fmt.Errorf("connection timed out %s %v %v", ws.remoteAddr.String(), time.Since(ts), ts),
				10*time.Second,
			),
		)
	}

	// if we're choked and not allowed to fast track any chunks then there is nothing
	// to do.
	if ws.PeerChoked && ws.peerfastset.IsEmpty() {
		return connwriterFlush(connwriteridle(ws), ws)
	}

	// detect effectively dead connections
	if timedout(ws.connection, ws.t.chunks.gracePeriod) {
		return cstate.Warning(
			t.next,
			errorsx.Timedout(
				errorsx.Errorf("c(%p) peer isnt sending chunks in a timely manner requests (%d > %d) last(%s)", ws, len(ws.requests), ws.requestsLowWater, ws.lastUsefulChunkReceived),
				10*time.Second,
			),
		)
	}

	return t.next
}

func connWriterSyncChunks(ws *writerstate) cstate.T {
	return _connWriterSyncChunks{writerstate: ws}
}

type _connWriterSyncChunks struct {
	*writerstate
}

func (t _connWriterSyncChunks) Update(ctx context.Context, _ *cstate.Shared) (r cstate.T) {
	ws := t.writerstate
	next := connWriterSyncComplete(ws)

	if ws.resync.Load().After(time.Now()) {
		return next
	}

	dup := ws.t.chunks.completed.Clone()
	dup.AndNot(ws.sentHaves)

	for i := dup.Iterator(); i.HasNext(); {
		piece := i.Next()
		ws.cmu().Lock()
		added := ws.sentHaves.CheckedAdd(piece)
		ws.cmu().Unlock()
		if !added {
			continue
		}

		_, err := ws.Post(pp.NewHavePiece(uint64(piece)))
		if err != nil {
			return cstate.Failure(err)
		}
	}

	return next
}

func connWriterSyncComplete(ws *writerstate) cstate.T {
	return _connWriterSyncComplete{writerstate: ws}
}

type _connWriterSyncComplete struct {
	*writerstate
}

func (t _connWriterSyncComplete) Update(ctx context.Context, _ *cstate.Shared) (r cstate.T) {
	ws := t.writerstate
	next := connwriterRequests(ws)

	if ws.t.chunks.Incomplete() {
		return next
	}

	writer := func(msg pp.Message) bool {
		if _, err := ws.Write(msg.MustMarshalBinary()); err != nil {
			ws.cfg.errors().Println(err)
			return false
		}

		ws.wroteMsg(&msg)
		return ws.writeBuffer.Len() < ws.bufferLimit
	}

	ws.SetInterested(false, writer)
	return next
}

func connwriterRequests(ws *writerstate) cstate.T {
	return _connwriterRequests{writerstate: ws}
}

type _connwriterRequests struct {
	*writerstate
}

func (t _connwriterRequests) Update(ctx context.Context, _ *cstate.Shared) (r cstate.T) {
	ws := t.writerstate

	writer := func(msg pp.Message) bool {
		if _, err := ws.Write(msg.MustMarshalBinary()); err != nil {
			ws.cfg.errors().Println(err)
			return false
		}

		ws.wroteMsg(&msg)
		return ws.writeBuffer.Len() < ws.bufferLimit
	}

	ws.checkFailures()
	available := ws.determineInterest(writer)
	ws.upload(writer)
	ws.genrequests(available, writer)

	// needresponse is tracking read that come in while we're in the critical section of this function
	// to prevent the state machine from going idle just because we didnt write anything this cycle.
	// needresponse tracks that a message can in that requires a message be sent.
	if ws.writeBuffer.Len() > 0 || ws.needsresponse.CompareAndSwap(true, false) {
		return connwriterFlush(
			connwriteractive(ws),
			ws,
		)
	}

	return connwriterKeepalive(ws)
}

func connwriterKeepalive(ws *writerstate) cstate.T {
	return _connwriterKeepalive{writerstate: ws}
}

type _connwriterKeepalive struct {
	*writerstate
}

func (t _connwriterKeepalive) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	var (
		err error
	)

	ws := t.writerstate

	if time.Since(ws.lastwrite) < ws.keepAliveTimeout {
		return connwriterFlush(connwriteridle(ws), ws)
	}

	if _, err = ws.Post(pp.NewKeepAlive()); err != nil {
		return cstate.Failure(errorsx.Wrap(err, "keepalive encoding failed"))
	}

	return connwriterFlush(connwriteridle(ws), ws)
}

func connwriterFlush(n cstate.T, ws *writerstate) cstate.T {
	return _connwriterFlush{next: n, writerstate: ws}
}

type _connwriterFlush struct {
	next cstate.T
	*writerstate
}

func (t _connwriterFlush) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	var (
		err error
	)

	ws := t.writerstate
	n, err := ws.Flush()

	if err != nil {
		return cstate.Failure(errorsx.Wrap(err, "failed to send requests"))
	}

	if n != 0 {
		ws.lastwrite = time.Now()
		ws.resync.Store(langx.Autoptr(ws.lastwrite.Add(ws.keepAliveTimeout)))
	}

	return t.next
}

func connwriteridle(ws *writerstate) cstate.T {
	return cstate.Idle(connwriteractive(ws), ws.keepAliveTimeout/2, ws.connection.respond, ws.connection.t.chunks.cond)
}

func connwriteractive(ws *writerstate) cstate.T {
	return connwriterclosed(ws, connWriterSyncChunks(ws))
}
