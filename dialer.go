package torrent

import (
	"context"
	"net"

	"golang.org/x/net/proxy"
)

// Dialers have the network locked in.
type Dialer interface {
	Dial(_ context.Context, addr string) (net.Conn, error)
	DialerNetwork() string
}

// Used by wrappers of standard library network types.
var DefaultNetDialer = &net.Dialer{}

// Adapts a DialContexter to the Dial interface in this package.
type NetworkDialer struct {
	Network string
	Dialer  proxy.ContextDialer
}

func (me NetworkDialer) DialerNetwork() string {
	return me.Network
}

func (me NetworkDialer) Dial(ctx context.Context, addr string) (_ net.Conn, err error) {
	return me.Dialer.DialContext(ctx, me.Network, addr)
}
