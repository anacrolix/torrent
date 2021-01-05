package utpx

import (
	"context"
	"net"
)

// Socket abstracts the utp Socket, so the implementation can be selected from
// different packages.
type Socket interface {
	net.PacketConn
	// net.Listener, but we can't have duplicate Close.
	Accept() (net.Conn, error)
	Addr() net.Addr
	// net.Dialer but there's no interface.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}
