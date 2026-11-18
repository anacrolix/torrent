package torrent

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestPendingIncomingHandshakesReserve(t *testing.T) {
	var p pendingIncomingHandshakes
	a := &net.TCPAddr{IP: net.ParseIP("1.2.3.4"), Port: 1}
	// Same IP, written as IPv4-mapped IPv6, different port.
	aMapped := &net.TCPAddr{IP: net.ParseIP("::ffff:1.2.3.4"), Port: 2}
	b := &net.TCPAddr{IP: net.ParseIP("5.6.7.8"), Port: 1}

	r1, ok := p.reserve(a, 3, 2)
	qt.Assert(t, qt.IsTrue(ok))
	r2, ok := p.reserve(aMapped, 3, 2)
	qt.Assert(t, qt.IsTrue(ok))
	_, ok = p.reserve(a, 3, 2)
	qt.Check(t, qt.IsFalse(ok), qt.Commentf("per-IP limit"))
	r3, ok := p.reserve(b, 3, 2)
	qt.Assert(t, qt.IsTrue(ok))
	_, ok = p.reserve(&net.TCPAddr{IP: net.ParseIP("9.9.9.9"), Port: 1}, 3, 2)
	qt.Check(t, qt.IsFalse(ok), qt.Commentf("total limit"))

	r1()
	// Releasing twice must not free a second slot.
	r1()
	_, ok = p.reserve(b, 3, 2)
	qt.Assert(t, qt.IsTrue(ok))
	_, ok = p.reserve(a, 3, 2)
	qt.Check(t, qt.IsFalse(ok))

	r2()
	r3()
	p.mu.Lock()
	qt.Check(t, qt.Equals(p.total, 1))
	qt.Check(t, qt.HasLen(p.byIp, 1))
	p.mu.Unlock()

	// Zero means unlimited.
	for range 10 {
		_, ok = p.reserve(a, 0, 0)
		qt.Assert(t, qt.IsTrue(ok))
	}
}

// Connections that never send a handshake must not be held beyond the per-IP limit.
func TestAcceptedConnsBeyondPendingHandshakeLimitAreClosed(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableUTP = true
	cfg.DisableIPv6 = true
	cfg.HandshakesTimeout = time.Minute
	cfg.MaxPendingIncomingHandshakesPerIp = 2
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	_, _ = cl.AddTorrentInfoHash(testutil.GreetingMetaInfo().HashInfoBytes())

	const numConns = 5
	var conns []net.Conn
	for range numConns {
		c, err := net.Dial("tcp", (&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: cl.LocalPort()}).String())
		qt.Assert(t, qt.IsNil(err))
		defer c.Close()
		conns = append(conns, c)
	}
	held := 0
	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := c.Read(make([]byte, 1))
		if errors.Is(err, os.ErrDeadlineExceeded) {
			held++
		} else {
			qt.Check(t, qt.IsTrue(err != nil), qt.Commentf("%v", err))
		}
	}
	qt.Check(t, qt.Equals(held, cfg.MaxPendingIncomingHandshakesPerIp))
}

// Runs the receive side of the handshake on a conn with the given network. The remote sends
// remoteSends and then nothing.
func runReceivedConnForAcceptLimiter(t *testing.T, network string, remoteSends []byte) *Client {
	cfg := TestingConfig(t)
	cfg.HandshakesTimeout = 50 * time.Millisecond
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	ours, theirs := net.Pipe()
	defer theirs.Close()
	go func() {
		theirs.Write(remoteSends)
		io.Copy(io.Discard, theirs)
	}()
	c := cl.newConnection(ours, newConnectionOpts{
		remoteAddr: &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 1},
		network:    network,
	})
	cl.runReceivedConn(c, func() {})
	return cl
}

// A uTP connection is accepted on one datagram whose source address can be forged. Such a
// connection timing out must not count against the source in the accept limiter.
func TestUnverifiedAcceptDoesNotCountAgainstSource(t *testing.T) {
	for _, tc := range []struct {
		network     string
		remoteSends []byte
		limited     bool
	}{
		{"udp", nil, false},
		{"udp", make([]byte, 100), true},
		{"tcp", nil, true},
	} {
		cl := runReceivedConnForAcceptLimiter(t, tc.network, tc.remoteSends)
		cl.lock()
		limited := len(cl.acceptLimiter) != 0
		cl.unlock()
		qt.Check(t, qt.Equals(limited, tc.limited), qt.Commentf("%+v", tc))
	}
}
