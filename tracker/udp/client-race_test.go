package udp

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reproduces the data race reported in
// https://github.com/erigontech/erigon/issues/20491: concurrent (*Client).Announce
// calls receiving an ActionError response race on the unlocked
// `cl.connIdIssued = time.Time{}` write inside (*Client).request — that write
// races with the cl.mu-protected write at the end of doConnectRoundTrip on
// another goroutine.
//
// The race is timing-sensitive — a single test invocation has roughly a 10%
// chance of catching it. Run with `go test -race -count=20` to amplify.
func TestRaceConcurrentAnnounceErrorResponse(t *testing.T) {
	t.Parallel()
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer srv.Close()

	go func() {
		buf := make([]byte, 0x800)
		for {
			n, addr, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 16 {
				continue
			}
			var rh RequestHeader
			if err := binary.Read(bytes.NewReader(buf[:n]), binary.BigEndian, &rh); err != nil {
				continue
			}
			var resp bytes.Buffer
			switch rh.Action {
			case ActionConnect:
				_ = Write(&resp, ResponseHeader{Action: ActionConnect, TransactionId: rh.TransactionId})
				_ = Write(&resp, ConnectionResponse{ConnectionId: 0xdeadbeef})
			default:
				_ = Write(&resp, ResponseHeader{Action: ActionError, TransactionId: rh.TransactionId})
				resp.WriteString(ConnectionIdMissmatchNul)
			}
			if _, err := srv.WriteTo(resp.Bytes(), addr); err != nil {
				return
			}
		}
	}()

	cc, err := NewConnClient(NewConnClientOpts{
		Network: "udp",
		Host:    srv.LocalAddr().String(),
	})
	require.NoError(t, err)
	defer cc.Close()

	// Force every request to attempt a fresh connect, maximising pressure on cl.connIdIssued.
	cc.Client.shouldReconnectOverride = func() bool { return true }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const goroutines = 32
	const perGoroutine = 200
	var ar AnnounceRequest
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				if ctx.Err() != nil {
					return
				}
				_, _, _ = cc.Announce(ctx, ar, Options{})
			}
		}()
	}
	wg.Wait()
}
