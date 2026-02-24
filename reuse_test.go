package torrent

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"
)

// Show that multiple connections from the same local TCP port to the same remote port will fail.
func TestTcpPortReuseIsABadIdea(t *testing.T) {
	remote, err := net.Listen("tcp", "localhost:0")
	qt.Assert(t, qt.IsNil(err))
	defer remote.Close()
	dialer := net.Dialer{}
	// Show that we can't duplicate an existing connection even with various socket options.
	dialer.Control = func(network, address string, c syscall.RawConn) (err error) {
		return c.Control(func(fd uintptr) {
			err = setReusePortSockOpts(fd)
		})
	}
	// Tie up a local port to the remote.
	first, err := dialer.Dial("tcp", remote.Addr().String())
	qt.Assert(t, qt.IsNil(err))
	defer first.Close()
	// Show that dialling the remote with the same local port fails.
	dialer.LocalAddr = first.LocalAddr()
	_, err = dialer.Dial("tcp", remote.Addr().String())
	qt.Assert(t, qt.IsNotNil(err))
	// Show that not fixing the local port again allows connections to succeed.
	dialer.LocalAddr = nil
	second, err := dialer.Dial("tcp", remote.Addr().String())
	qt.Assert(t, qt.IsNil(err))
	second.Close()
}

// Show that multiple connections from the same local utp socket to the same remote port will
// succeed. This is necessary for ut_holepunch to work.
func TestUtpLocalPortIsReusable(t *testing.T) {
	const network = "udp"
	remote, err := NewUtpSocket(network, "localhost:0", nil, log.Default)
	qt.Assert(t, qt.IsNil(err))
	defer remote.Close()
	var remoteAccepts int32
	doneAccepting := make(chan struct{})
	go func() {
		defer close(doneAccepting)
		for {
			c, err := remote.Accept()
			if err != nil {
				if atomic.LoadInt32(&remoteAccepts) != 2 {
					t.Logf("error accepting on remote: %v", err)
				}
				break
			}
			// This is not a leak, bugger off.
			defer c.Close()
			atomic.AddInt32(&remoteAccepts, 1)
		}
	}()
	local, err := NewUtpSocket(network, "localhost:0", nil, log.Default)
	qt.Assert(t, qt.IsNil(err))
	defer local.Close()
	first, err := local.DialContext(context.Background(), network, remote.Addr().String())
	qt.Assert(t, qt.IsNil(err))
	defer first.Close()
	second, err := local.DialContext(context.Background(), network, remote.Addr().String())
	qt.Assert(t, qt.IsNil(err))
	defer second.Close()
	remote.Close()
	<-doneAccepting
	qt.Assert(t, qt.Equals(atomic.LoadInt32(&remoteAccepts), int32(2)))
}
