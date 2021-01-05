package sockets

import (
	"context"
	"net"

	"github.com/james-lawrence/torrent/internal/netx"
)

// New creates a socket from a net listener and dialer.
func New(l net.Listener, d netx.ContextDialer) Socket {
	if l, ok := l.(packetlistener); ok {
		return packetconnSocket{packetlistener: l, dialer: d}
	}

	return socket{Listener: l, dialer: d}
}

// Socket for torrent clients
type Socket interface {
	net.Listener
	Dial(ctx context.Context, addr string) (conn net.Conn, err error)
}

type packetlistener interface {
	net.PacketConn
	// Accept waits for and returns the next connection to the listener.
	Accept() (net.Conn, error)
	// Addr returns the listener's network address.
	Addr() net.Addr
}

type packetconnSocket struct {
	packetlistener
	dialer netx.ContextDialer
}

// Dial remote peers from this socket.
func (t packetconnSocket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	return t.dialer.DialContext(ctx, t.packetlistener.Addr().Network(), addr)
}

type socket struct {
	net.Listener
	dialer netx.ContextDialer
}

// Dial remote peers from this socket.
func (t socket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	return t.dialer.DialContext(ctx, t.Listener.Addr().Network(), addr)
}
