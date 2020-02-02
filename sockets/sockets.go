package sockets

import (
	"context"
	"net"
)

type dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// New creates a socket from a net listener and dialer.
func New(l net.Listener, d dialer) Socket {
	return Socket{Listener: l, dialer: d}
}

// Socket for torrent clients.
type Socket struct {
	net.Listener
	dialer
}

// Dial remote peers from this socket.
func (t Socket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	return t.dialer.DialContext(ctx, t.Listener.Addr().Network(), addr)
}
