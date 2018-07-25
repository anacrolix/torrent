package torrent

import (
	"net"
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testListenerNetwork(
	t *testing.T,
	listenFunc func(net, addr string) (net.Listener, error),
	expectedNet, givenNet, addr string, validIp4 bool,
) {
	l, err := listenFunc(givenNet, addr)
	require.NoError(t, err)
	defer l.Close()
	assert.EqualValues(t, expectedNet, l.Addr().Network())
	ip := missinggo.AddrIP(l.Addr())
	assert.Equal(t, validIp4, ip.To4() != nil, ip)
}

func listenUtpListener(net, addr string) (l net.Listener, err error) {
	l, err = NewUtpSocket(net, addr, nil)
	return
}

func testAcceptedConnAddr(
	t *testing.T,
	network string, valid4 bool,
	dial func(addr string) (net.Conn, error),
	listen func() (net.Listener, error),
) {
	l, err := listen()
	require.NoError(t, err)
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, err := dial(l.Addr().String())
		require.NoError(t, err)
		<-done
		c.Close()
	}()
	c, err := l.Accept()
	require.NoError(t, err)
	defer c.Close()
	assert.EqualValues(t, network, c.RemoteAddr().Network())
	assert.Equal(t, valid4, missinggo.AddrIP(c.RemoteAddr()).To4() != nil)
}

func listenClosure(rawListenFunc func(string, string) (net.Listener, error), network, addr string) func() (net.Listener, error) {
	return func() (net.Listener, error) {
		return rawListenFunc(network, addr)
	}
}

func dialClosure(f func(net, addr string) (net.Conn, error), network string) func(addr string) (net.Conn, error) {
	return func(addr string) (net.Conn, error) {
		return f(network, addr)
	}
}

func TestListenLocalhostNetwork(t *testing.T) {
	testListenerNetwork(t, net.Listen, "tcp", "tcp", "0.0.0.0:0", false)
	testListenerNetwork(t, net.Listen, "tcp", "tcp", "[::1]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp6", "[::1]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp6", "[::]:0", false)
	testListenerNetwork(t, listenUtpListener, "udp", "udp4", "localhost:0", true)

	testAcceptedConnAddr(t, "tcp", false, dialClosure(net.Dial, "tcp"), listenClosure(net.Listen, "tcp6", ":0"))
}
