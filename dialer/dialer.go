package dialer

import (
	"context"
	"net"
)

// Dialers have the network locked in.
type T interface {
	Dial(_ context.Context, addr string) (net.Conn, error)
	DialerNetwork() string
}

// An interface to ease wrapping dialers that explicitly include a network parameter.
type WithContext interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Used by wrappers of standard library network types.
var Default = &net.Dialer{}

// Adapts a WithContext to the Dial interface in this package.
type WithNetwork struct {
	Network string
	Dialer  WithContext
}

func (me WithNetwork) DialerNetwork() string {
	return me.Network
}

func (me WithNetwork) Dial(ctx context.Context, addr string) (_ net.Conn, err error) {
	return me.Dialer.DialContext(ctx, me.Network, addr)
}
