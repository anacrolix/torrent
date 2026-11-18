package udp

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

// Answers connect and announce requests from any client, with no peers.
func serveFakeTracker(t *testing.T) net.PacketConn {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { pc.Close() })
	go func() {
		b := make([]byte, 0x800)
		for {
			n, addr, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			r := bytes.NewReader(b[:n])
			var h RequestHeader
			if Read(r, &h) != nil {
				continue
			}
			var resp bytes.Buffer
			Write(&resp, ResponseHeader{Action: h.Action, TransactionId: h.TransactionId})
			switch h.Action {
			case ActionConnect:
				Write(&resp, ConnectionResponse{ConnectionId: 42})
			case ActionAnnounce:
				Write(&resp, AnnounceResponseHeader{Interval: 1800})
			}
			pc.WriteTo(resp.Bytes(), addr)
		}
	}()
	return pc
}

func TestSocketSharedByTrackers(t *testing.T) {
	var mu sync.Mutex
	var listens int
	s := &Socket{
		Network: "udp4",
		ListenPacket: func(network, addr string) (net.PacketConn, error) {
			mu.Lock()
			listens++
			mu.Unlock()
			return net.ListenPacket(network, addr)
		},
	}
	defer s.Close()
	qt.Assert(t, qt.IsNil(s.LocalAddr()))
	const numTrackers = 20
	var wg sync.WaitGroup
	for range numTrackers {
		srv := serveFakeTracker(t)
		cc, err := NewConnClient(NewConnClientOpts{Host: srv.LocalAddr().String(), Socket: s})
		qt.Assert(t, qt.IsNil(err))
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h, _, err := cc.Announce(ctx, AnnounceRequest{}, Options{})
			qt.Check(t, qt.IsNil(err))
			qt.Check(t, qt.Equals(h.Interval, 1800))
			// Doesn't close the shared socket.
			qt.Check(t, qt.IsNil(cc.Close()))
		}()
	}
	wg.Wait()
	qt.Assert(t, qt.Equals(listens, 1))
	qt.Assert(t, qt.IsNotNil(s.LocalAddr()))
	qt.Assert(t, qt.IsNil(s.Close()))
	cc, err := NewConnClient(NewConnClientOpts{Host: "127.0.0.1:1", Socket: s})
	qt.Assert(t, qt.IsNil(err))
	_, _, err = cc.Announce(context.Background(), AnnounceRequest{}, Options{})
	qt.Assert(t, qt.ErrorIs(err, errSocketClosed))
}

// A Socket opens a new conn after the old one fails.
func TestSocketReopensAfterReadError(t *testing.T) {
	var mu sync.Mutex
	var conns []net.PacketConn
	s := &Socket{
		Network: "udp4",
		ListenPacket: func(network, addr string) (net.PacketConn, error) {
			pc, err := net.ListenPacket(network, addr)
			if err == nil {
				mu.Lock()
				conns = append(conns, pc)
				mu.Unlock()
			}
			return pc, err
		},
	}
	defer s.Close()
	srv := serveFakeTracker(t)
	cc, err := NewConnClient(NewConnClientOpts{Host: srv.LocalAddr().String(), Socket: s})
	qt.Assert(t, qt.IsNil(err))
	announce := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, err := cc.Announce(ctx, AnnounceRequest{}, Options{})
		qt.Assert(t, qt.IsNil(err))
	}
	announce()
	mu.Lock()
	conns[0].Close()
	mu.Unlock()
	for s.LocalAddr() != nil {
		time.Sleep(time.Millisecond)
	}
	// Force a new connect round trip too.
	cc.Client.connIdIssued = time.Time{}
	announce()
	mu.Lock()
	defer mu.Unlock()
	qt.Assert(t, qt.HasLen(conns, 2))
}

// Answers every request three times.
type duplicateResponseWriter struct {
	d *Dispatcher
}

func (w duplicateResponseWriter) Write(b []byte) (int, error) {
	var h RequestHeader
	if err := Read(bytes.NewReader(b), &h); err != nil {
		return 0, err
	}
	var resp bytes.Buffer
	Write(&resp, ResponseHeader{Action: h.Action, TransactionId: h.TransactionId})
	switch h.Action {
	case ActionConnect:
		Write(&resp, ConnectionResponse{ConnectionId: 42})
	case ActionAnnounce:
		Write(&resp, AnnounceResponseHeader{Interval: 1800})
	}
	go func() {
		// All run on one goroutine, like a socket's read loop.
		for range 3 {
			_ = w.d.Dispatch(resp.Bytes(), nil)
		}
	}()
	return len(b), nil
}

// A response received more than once must not block the dispatcher, which would stop every later request.
func TestDuplicateResponses(t *testing.T) {
	var d Dispatcher
	cl := Client{Dispatcher: &d, Writer: duplicateResponseWriter{&d}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 3 {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Make every announce do a connect round trip first.
			cl.connIdIssued = time.Time{}
			_, _, err := cl.Announce(ctx, AnnounceRequest{}, Options{}, func(net.Addr) bool { return false })
			cancel()
			qt.Check(t, qt.IsNil(err))
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("announces deadlocked")
	}
}
