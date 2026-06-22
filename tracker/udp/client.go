package udp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/log"
	"github.com/protolambda/ctxlock"
)

// Client interacts with UDP trackers via its Writer and Dispatcher. It has no knowledge of
// connection specifics.
type Client struct {
	// Guards connId / connIdIssued / connSeq. Held only for short, in-memory critical
	// sections — never across network I/O.
	stateMu sync.Mutex
	// Serialises actual connect round-trips. Held across the connect's network I/O so a burst
	// of expirations causes one roundtrip rather than N. Cancellable so a goroutine waiting
	// for an in-flight connect can bail when its ctx is done.
	connectMu ctxlock.Lock

	connId ConnectionId
	// Set to zero if connId is invalidated or expired (you can still use connId, but it's a hint
	// to trigger a reconnect).
	connIdIssued time.Time
	// Local-only value tracking connection ID issuance. Incremented on connect replies.
	connSeq connSeq

	shouldReconnectOverride func() bool

	Dispatcher *Dispatcher
	Writer     io.Writer
}

type connSeq int

func (cl *Client) Announce(
	ctx context.Context, req AnnounceRequest, opts Options,
	// Decides whether the response body is IPv6 or IPv4, see BEP 15.
	ipv6 func(net.Addr) bool,
) (
	respHdr AnnounceResponseHeader,
	// A slice of krpc.NodeAddr, likely wrapped in an appropriate unmarshalling wrapper.
	peers AnnounceResponsePeers,
	err error,
) {
	respBody, addr, err := cl.actionRequest(ctx, ActionAnnounce, append(mustMarshal(req), opts.Encode()...))
	if err != nil {
		return
	}
	r := bytes.NewBuffer(respBody)
	err = Read(r, &respHdr)
	if err != nil {
		err = fmt.Errorf("reading response header: %w", err)
		return
	}
	if ipv6(addr) {
		peers = &krpc.CompactIPv6NodeAddrs{}
	} else {
		peers = &krpc.CompactIPv4NodeAddrs{}
	}
	err = peers.UnmarshalBinary(r.Bytes())
	if err != nil {
		err = fmt.Errorf("reading response peers: %w", err)
	}
	return
}

// There's no way to pass options in a scrape, since we don't when the request body ends.
func (cl *Client) Scrape(
	ctx context.Context, ihs []InfoHash,
) (
	out ScrapeResponse, err error,
) {
	respBody, _, err := cl.actionRequest(ctx, ActionScrape, mustMarshal(ScrapeRequest(ihs)))
	if err != nil {
		return
	}
	r := bytes.NewBuffer(respBody)
	for r.Len() != 0 {
		var item ScrapeInfohashResult
		err = Read(r, &item)
		if err != nil {
			return
		}
		out = append(out, item)
	}
	if len(out) > len(ihs) {
		err = fmt.Errorf("got %v results but expected %v", len(out), len(ihs))
		return
	}
	return
}

// actionRequest runs an Announce or Scrape: get a connId, send the request, and on a
// "Connection ID missmatch" reply, retry once. resetConn no-ops when another goroutine has
// already reconnected, so the retry naturally reuses the newer connId rather than forcing
// yet another connect.
func (cl *Client) actionRequest(
	ctx context.Context, action Action, body []byte,
) (respBody []byte, addr net.Addr, err error) {
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var connId ConnectionId
		var seq connSeq
		connId, seq, err = cl.ensureConnId(ctx)
		if err != nil {
			return
		}
		respBody, addr, err = cl.request(ctx, action, body, connId)
		if err == nil {
			return
		}
		if !isConnIdMismatch(err) {
			return
		}
		cl.resetConn(seq)
	}
	return
}

func isConnIdMismatch(err error) bool {
	var er ErrorResponse
	return errors.As(err, &er) && er.Message == ConnectionIdMissmatchNul
}

// shouldReconnectLocked reports whether a fresh connect is needed. Caller must hold stateMu.
func (cl *Client) shouldReconnectLocked() bool {
	if cl.shouldReconnectOverride != nil {
		return cl.shouldReconnectOverride()
	}
	return cl.connIdIssued.IsZero() || time.Since(cl.connIdIssued) >= time.Minute
}

// ensureConnId returns a connection ID valid right now, performing a connect round-trip if
// necessary. seq is the issuance generation; pass it to resetConn(seq) to invalidate without
// clobbering a fresher connect issued by another goroutine.
//
// Caller must NOT hold stateMu or connectMu.
func (cl *Client) ensureConnId(ctx context.Context) (id ConnectionId, seq connSeq, err error) {
	cl.stateMu.Lock()
	if !cl.shouldReconnectLocked() {
		id, seq = cl.connId, cl.connSeq
		cl.stateMu.Unlock()
		return
	}
	cl.stateMu.Unlock()

	if err = cl.connectMu.LockCtx(ctx); err != nil {
		err = fmt.Errorf("locking connection: %w", err)
		return
	}
	defer cl.connectMu.Unlock()

	// Another goroutine may have completed a connect while we were waiting on connectMu.
	cl.stateMu.Lock()
	if !cl.shouldReconnectLocked() {
		id, seq = cl.connId, cl.connSeq
		cl.stateMu.Unlock()
		return
	}
	cl.stateMu.Unlock()

	return cl.doConnectRoundTrip(ctx)
}

// doConnectRoundTrip does the connect request and updates local state if it succeeds. Returns
// the freshly-issued (id, seq). Caller MUST hold connectMu (this is what serialises concurrent
// connects). stateMu is taken briefly only to publish the result.
func (cl *Client) doConnectRoundTrip(ctx context.Context) (id ConnectionId, seq connSeq, err error) {
	respBody, _, err := cl.request(ctx, ActionConnect, nil, ConnectRequestConnectionId)
	if err != nil {
		return
	}
	var connResp ConnectionResponse
	err = binary.Read(bytes.NewReader(respBody), binary.BigEndian, &connResp)
	if err != nil {
		return
	}
	cl.stateMu.Lock()
	cl.connId = connResp.ConnectionId
	cl.connIdIssued = time.Now()
	cl.connSeq++
	id, seq = cl.connId, cl.connSeq
	cl.stateMu.Unlock()
	return
}

func (cl *Client) writeRequest(
	ctx context.Context, action Action, body []byte, tId TransactionId, connId ConnectionId, buf *bytes.Buffer,
) (
	err error,
) {
	buf.Reset()
	err = Write(buf, RequestHeader{
		ConnectionId:  connId,
		Action:        action,
		TransactionId: tId,
	})
	if err != nil {
		panic(err)
	}
	buf.Write(body)
	_, err = cl.Writer.Write(buf.Bytes())
	return
}

// resetConn marks the connection ID as expired so the next ensureConnId reconnects. If
// cl.connSeq has advanced past seq, another goroutine has already reconnected — nothing to
// do, the next caller will see the fresher connId.
func (cl *Client) resetConn(seq connSeq) {
	cl.stateMu.Lock()
	defer cl.stateMu.Unlock()
	if cl.connSeq == seq {
		cl.connIdIssued = time.Time{}
	}
}

func (cl *Client) requestWriter(
	ctx context.Context,
	action Action,
	body []byte,
	tId TransactionId,
	connId ConnectionId,
) (err error) {
	var buf bytes.Buffer
	for n := 0; ; n++ {
		err = cl.writeRequest(ctx, action, body, tId, connId, &buf)
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout(n)):
		}
	}
}

const ConnectionIdMissmatchNul = "Connection ID missmatch.\x00"

type ErrorResponse struct {
	Message string
}

func (me ErrorResponse) Error() string {
	return fmt.Sprintf("error response: %#q", me.Message)
}

// request sends a single tracker request with the provided connId and waits for the matching
// response. It does not touch stateMu or connectMu. Connection-ID expiry is the caller's
// concern (via ensureConnId / resetConn).
func (cl *Client) request(
	ctx context.Context,
	action Action,
	body []byte,
	connId ConnectionId,
) (respBody []byte, addr net.Addr, err error) {
	respChan := make(chan DispatchedResponse, 1)
	t := cl.Dispatcher.NewTransaction(func(dr DispatchedResponse) {
		respChan <- dr
	})
	defer t.End()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- cl.requestWriter(ctx, action, body, t.Id(), connId)
	}()
	select {
	case dr := <-respChan:
		switch dr.Header.Action {
		case action:
			respBody = dr.Body
			addr = dr.Addr
		case ActionError:
			// udp://tracker.torrent.eu.org:451/announce frequently returns "Connection ID
			// missmatch.\x00"
			stringBody := string(dr.Body)
			err = ErrorResponse{Message: stringBody}
			if stringBody == ConnectionIdMissmatchNul {
				err = log.WithLevel(log.Debug, err)
			}
		default:
			err = fmt.Errorf("unexpected response action %v", dr.Header.Action)
		}
	case err = <-writeErr:
		err = fmt.Errorf("write error: %w", err)
	case <-ctx.Done():
		err = context.Cause(ctx)
	}
	return
}
