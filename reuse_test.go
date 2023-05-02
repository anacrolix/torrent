package torrent

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/anacrolix/log"

	qt "github.com/frankban/quicktest"
)

// Show that multiple connections from the same local TCP port to the same remote port will fail.
func TestTcpPortReuseIsABadIdea(t *testing.T) {
	remote, err := net.Listen("tcp", "localhost:0")
	c := qt.New(t)
	c.Assert(err, qt.IsNil)
	defer remote.Close()
	dialer := net.Dialer{}
	dialer.Control = func(network, address string, c syscall.RawConn) (err error) {
		return c.Control(func(fd uintptr) {
			err = setReusePortSockOpts(fd)
		})
	}
	first, err := dialer.Dial("tcp", remote.Addr().String())
	c.Assert(err, qt.IsNil)
	defer first.Close()
	dialer.LocalAddr = first.LocalAddr()
	_, err = dialer.Dial("tcp", remote.Addr().String())
	c.Assert(errors.Is(err, syscall.EADDRINUSE), qt.IsTrue)
}

// Show that multiple connections from the same local utp socket to the same remote port will
// succeed. This is necessary for ut_holepunch to work.
func TestUtpLocalPortIsReusable(t *testing.T) {
	const network = "udp"
	c := qt.New(t)
	remote, err := NewUtpSocket(network, "localhost:0", nil, log.Default)
	c.Assert(err, qt.IsNil)
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
	c.Assert(err, qt.IsNil)
	defer local.Close()
	first, err := local.DialContext(context.Background(), network, remote.Addr().String())
	c.Assert(err, qt.IsNil)
	defer first.Close()
	second, err := local.DialContext(context.Background(), network, remote.Addr().String())
	c.Assert(err, qt.IsNil)
	defer second.Close()
	remote.Close()
	<-doneAccepting
	c.Assert(atomic.LoadInt32(&remoteAccepts), qt.Equals, int32(2))
}
