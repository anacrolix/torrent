package torrent

import (
	"context"
	"net"

	"github.com/pkg/errors"
)

type firewallCallback func(net.Addr) bool

type dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

type socket interface {
	net.Listener
	dialer
}

// Binder binds network sockets to the client.
type Binder interface {
	// Bind to the given client if err is nil.
	Bind(cl *Client, err error) (*Client, error)
}

// NewSocketsBind binds a set of sockets to the client.
// it bypasses any disable checks (tcp,udp, ip4/6) from the configuration.
func NewSocketsBind(s ...socket) Binder {
	return socketsBind(s)
}

type socketsBind []socket

// Bind the client to available networks. consumes the result of NewClient.
func (t socketsBind) Bind(cl *Client, err error) (*Client, error) {

	if err != nil {
		return nil, err
	}

	if len(t) == 0 {
		cl.Close()
		return nil, errors.Errorf("at least one socket is required")
	}

	for _, s := range t {
		if err = cl.Bind(s); err != nil {
			cl.Close()
			return nil, err
		}
	}

	return cl, nil
}
