package udp

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

// An error response to a connect request is handled while Client.mu is held by the writer of the
// request that needed the connection, so it must not try to take the lock again.
func TestConnectErrorResponseDoesNotDeadlock(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	qt.Assert(t, qt.IsNil(err))
	defer pc.Close()
	go func() {
		b := make([]byte, 0x800)
		for {
			n, addr, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			var h RequestHeader
			if binary.Read(bytes.NewReader(b[:n]), binary.BigEndian, &h) != nil {
				continue
			}
			var resp bytes.Buffer
			Write(&resp, ResponseHeader{Action: ActionError, TransactionId: h.TransactionId})
			resp.WriteString("error\x00")
			pc.WriteTo(resp.Bytes(), addr)
		}
	}()
	cc, err := NewConnClient(NewConnClientOpts{Network: "udp4", Host: pc.LocalAddr().String()})
	qt.Assert(t, qt.IsNil(err))
	defer cc.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := cc.Announce(ctx, AnnounceRequest{}, Options{})
		done <- err
	}()
	select {
	case err := <-done:
		qt.Check(t, qt.IsNotNil(err))
	case <-time.After(5 * time.Second):
		t.Fatal("announce deadlocked after a connect error response")
	}
}
