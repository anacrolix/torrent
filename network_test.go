package torrent

import (
	"net"
	"testing"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
	qt "github.com/go-quicktest/qt"
)

func testListenerNetwork(
	t *testing.T,
	listenFunc func(net, addr string) (net.Listener, error),
	expectedNet, givenNet, addr string, validIp4 bool,
) {
	l, err := listenFunc(givenNet, addr)
	if isUnsupportedNetworkError(err) {
		return
	}
	qt.Assert(t, qt.IsNil(err))
	defer l.Close()
	qt.Check(t, qt.Equals(l.Addr().Network(), expectedNet))
	ip := missinggo.AddrIP(l.Addr())
	qt.Check(t, qt.Equals(ip.To4() != nil, validIp4), qt.Commentf("%v", ip))
}

func listenUtpListener(net, addr string) (l net.Listener, err error) {
	l, err = NewUtpSocket(net, addr, nil, log.Default)
	return
}

func testAcceptedConnAddr(
	t *testing.T,
	network string, valid4 bool,
	dial func(addr string) (net.Conn, error),
	listen func() (net.Listener, error),
) {
	l, err := listen()
	qt.Assert(t, qt.IsNil(err))
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, err := dial(l.Addr().String())
		qt.Assert(t, qt.IsNil(err))
		<-done
		c.Close()
	}()
	c, err := l.Accept()
	qt.Assert(t, qt.IsNil(err))
	defer c.Close()
	qt.Check(t, qt.Equals(c.RemoteAddr().Network(), network))
	qt.Check(t, qt.Equals(missinggo.AddrIP(c.RemoteAddr()).To4() == nil, valid4))
}

func listenClosure(
	rawListenFunc func(string, string) (net.Listener, error),
	network, addr string,
) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		return rawListenFunc(network, addr)
	}
}

func dialClosure(f func(net, addr string) (net.Conn, error), network string) func(addr string) (net.Conn, error) {
	return func(addr string) (net.Conn, error) {
		return f(network, addr)
	}
}

// Reports whether the host can open IPv6 sockets at all. Some sandboxes and containers can't.
func hostSupportsIpv6() bool {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func TestListenLocalhostNetwork(t *testing.T) {
	// With IPv6 available, Go listens dual-stack on "::" for "tcp" and "0.0.0.0". Without it, it
	// falls back to an IPv4 socket.
	testListenerNetwork(t, net.Listen, "tcp", "tcp", "0.0.0.0:0", !hostSupportsIpv6())
	testListenerNetwork(t, net.Listen, "tcp", "tcp", "[::1]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp6", "[::1]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp6", "[::]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp4", "localhost:0", true)

	testAcceptedConnAddr(
		t,
		"tcp",
		false,
		dialClosure(net.Dial, "tcp"),
		listenClosure(net.Listen, "tcp4", "localhost:0"),
	)
}

func TestIsUnsupportedNetworkError(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation (RFC 5737), so no interface should have it.
	l, err := net.Listen("tcp4", "192.0.2.1:0")
	if err == nil {
		l.Close()
		t.Skip("host can bind 192.0.2.1")
	}
	qt.Check(t, qt.IsTrue(isUnsupportedNetworkError(err)), qt.Commentf("%v", err))
	qt.Check(t, qt.IsFalse(isUnsupportedNetworkError(nil)))
	// A port that's in use is a real failure, not an unsupported network.
	l, err = net.Listen("tcp4", "127.0.0.1:0")
	qt.Assert(t, qt.IsNil(err))
	defer l.Close()
	_, err = net.Listen("tcp4", l.Addr().String())
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsFalse(isUnsupportedNetworkError(err)), qt.Commentf("%v", err))
}
