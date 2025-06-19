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

// See the order given in Transmission's tr_peerMsgsNew.
func ConnExtensions(ctx context.Context, cn *connection) error {
	return cstate.Run(ctx, connexinit(cn, connexfast(cn, connexdht(cn, nil))))
}

func connexinit(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		if !(cn.PeerExtensionBytes.SupportsExtended() && cn.t.cln.extensionBytes.SupportsExtended()) {
			return n
		}

		msg := pp.ExtendedHandshakeMessage{
			M:            cn.t.cln.config.extensions,
			V:            cn.t.cln.config.ExtendedHandshakeClientVersion,
			Reqq:         cn.t.cln.config.maximumOutstandingRequests,
			YourIp:       pp.CompactIp(cn.remoteAddr.Addr().AsSlice()),
			Encryption:   cn.t.cln.config.HeaderObfuscationPolicy.Preferred || !cn.t.cln.config.HeaderObfuscationPolicy.RequirePreferred,
			Port:         cn.t.cln.incomingPeerPort(),
			MetadataSize: cn.t.metadataSize(),
			// TODO: We can figured these out specific to the socket
			// used.
			Ipv4: pp.CompactIp(cn.t.cln.config.publicIP4.To4()),
			Ipv6: cn.t.cln.config.publicIP6.To16(),
		}

		encoded, err := bencode.Marshal(msg)
		if err != nil {
			return cstate.Failure(errorsx.Wrapf(err, "unable to encode message %T", msg))
		}

		_, err = cn.Post(pp.Message{
			Type:            pp.Extended,
			ExtendedID:      pp.HandshakeExtendedID,
			ExtendedPayload: encoded,
		})

		if err != nil {
			return cstate.Failure(errorsx.Wrapf(err, "unable to encode message %T", msg))
		}

		return n
	})
}

func connexfast(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		if _, err := cn.PostBitfield(); err != nil {
			return cstate.Failure(err)
		}

		if !cn.fastEnabled() {
			return n
		}

		switch cn.t.chunks.completed.GetCardinality() {
		case 0:
			if _, err := cn.Post(pp.Message{Type: pp.HaveNone}); err != nil {
				return cstate.Failure(err)
			}
			cn.sentHaves.Clear()
			return n
		case cn.t.chunks.pieces:
			if _, err := cn.Post(pp.Message{Type: pp.HaveAll}); err != nil {
				return cstate.Failure(err)
			}
			cn.sentHaves.AddRange(0, cn.t.chunks.pieces)
			fastset := errorsx.Must(bep0006.AllowedFastSet(cn.remoteAddr.Addr(), cn.t.md.ID, cn.t.chunks.pieces, min(32, cn.t.chunks.pieces)))
			for _, v := range fastset.ToArray() {
				if _, err := cn.Post(pp.NewAllowedFast(v)); err != nil {
					return cstate.Failure(err)
				}
			}
			return n
		}

		return n
	})
}

func connexdht(cn *connection, n cstate.T) cstate.T {
	return cstate.Fn(func(context.Context, *cstate.Shared) cstate.T {
		if !(cn.PeerExtensionBytes.SupportsDHT() && cn.t.cln.extensionBytes.SupportsDHT() && cn.t.cln.haveDhtServer()) {
			return n
		}

		_, err := cn.Post(pp.Message{
			Type: pp.Port,
			Port: cn.t.cln.dhtPort(),
		})
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
	cn.t.cln.config.info().Printf("c(%p) writer initiated\n", cn)
	defer cn.t.cln.config.info().Printf("c(%p) writer completed\n", cn)
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

	return cstate.Run(ctx, connWriterSyncChunks(ws))
}

type writerstate struct {
	*connection
	bufferLimit      int
	keepAliveTimeout time.Duration
	lastwrite        time.Time
	resync           *atomic.Pointer[time.Time]
}

func (t *writerstate) String() string {
	return fmt.Sprintf("%p seed(%t)", t.connection, t.connection.t.seeding())
}

func connwriterclosed(ws *writerstate, next cstate.T) cstate.T {
	return _connWriterClosed{writerstate: ws, next: next}
}

type _connWriterClosed struct {
	*writerstate
	next cstate.T
}

func (t _connWriterClosed) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	defer log.Printf("c(%p) seed(%t) check shutdown completed\n", t.connection, t.t.seeding())
	ws := t.writerstate

	// delete requests that were requested beyond the timeout.
	timedout := func(cn *connection, grace time.Duration) bool {
		ts := time.Now()
		return cn.lastUsefulChunkReceived.Add(grace).Before(ts) && cn.t.chunks.Missing() > 0
	}

	if ws.closed.Load() {
		return nil
	}

	if since := time.Since(ws.lastMessageReceived); since > 2*ws.keepAliveTimeout {
		return cstate.Failure(fmt.Errorf("connection timed out %s %v %v", ws.remoteAddr.String(), since, ws.lastMessageReceived))
	}

	// if we're choked and not allowed to fast track any chunks then there is nothing
	// to do.
	if ws.PeerChoked && ws.fastset.IsEmpty() {
		return connwriteridle(ws)
	}

	// detect effectively dead connections
	if timedout(ws.connection, ws.t.chunks.gracePeriod) {
		return cstate.Failure(errorsx.Errorf("c(%p) peer isnt sending chunks - space unavailable(%d > %d) missing(%t) last(%s)", ws, len(ws.requests), ws.requestsLowWater, ws.t.chunks.Missing() == 0, ws.lastUsefulChunkReceived))
	}

	return t.next
}

func connWriterSyncChunks(ws *writerstate) cstate.T {
	return _connWriterSyncChunks{writerstate: ws}
}

type _connWriterSyncChunks struct {
	*writerstate
}

func (t _connWriterSyncChunks) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	ws := t.writerstate
	next := connwriterRequests(ws)

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

		_, err := ws.Post(pp.Message{
			Type:  pp.Have,
			Index: pp.Integer(piece),
		})
		if err != nil {
			return cstate.Failure(err)
		}
	}

	return next
}

func connwriterRequests(ws *writerstate) cstate.T {
	return _connwriterRequests{writerstate: ws}
}

type _connwriterRequests struct {
	*writerstate
}

func (t _connwriterRequests) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	ws := t.writerstate
	writer := func(msg pp.Message) bool {
		ws.wroteMsg(&msg)
		return ws.writeBuffer.Len() < ws.bufferLimit
	}

	ws.checkFailures()
	ws.fillWriteBuffer(writer)
	ws.upload(writer)

	if ws.writeBuffer.Len() > 0 || time.Since(ws.lastwrite) < ws.keepAliveTimeout {
		return connwriterFlush(ws)
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
	if err = bencode.NewEncoder(ws.writeBuffer).Encode(pp.Message{Keepalive: true}); err != nil {
		return cstate.Failure(errorsx.Wrap(err, "keepalive encoding failed"))
	}

	return connwriterFlush(ws)
}

func connwriterFlush(ws *writerstate) cstate.T {
	return _connwriterFlush{writerstate: ws}
}

type _connwriterFlush struct {
	*writerstate
}

func (t _connwriterFlush) Update(ctx context.Context, _ *cstate.Shared) cstate.T {
	var (
		err error
	)

	ws := t.writerstate
	ws.cmu().Lock()
	buf := ws.writeBuffer.Bytes()
	ws.writeBuffer.Reset()
	n, err := ws.w.Write(buf)
	ws.cmu().Unlock()

	defer log.Printf("--------------------------------------------------------- flush completed %s - %d ---------------------------------------------------------\n", ws, len(buf))

	if err != nil {
		return cstate.Failure(errorsx.Wrap(err, "failed to send requests"))
	}

	if n != len(buf) {
		return cstate.Failure(errorsx.Errorf("write failed written != len(buf) (%d != %d)", n, len(buf)))
	}

	if n != 0 {
		ws.lastwrite = time.Now()
		ws.resync.Store(langx.Autoptr(ws.lastwrite.Add(ws.keepAliveTimeout)))
	}

	return connwriteridle(ws)
}

func connwriteridle(ws *writerstate) cstate.T {
	return cstate.Idle(connwriterclosed(ws, connWriterSyncChunks(ws)), ws.connection.writerCond, ws.keepAliveTimeout/2)
}
