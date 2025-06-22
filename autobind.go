package torrent

import (
	"context"
	"errors"
	"net"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/internal/langx"
	"github.com/james-lawrence/torrent/sockets"
)

type dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

// Binder binds network sockets to the client.
type Binder interface {
	// Bind to the given client if err is nil.
	Bind(cl *Client, err error) (*Client, error)
	Close() error
}

type BinderOption func(v *binder)

// EnableDHT enables DHT.
func BinderOptionDHT(a *binder) {
	a.EnableDHT = true
}

// NewSocketsBind binds a set of sockets to the client.
// it bypasses any disable checks (tcp,udp, ip4/6) from the configuration.
func NewSocketsBind(s ...sockets.Socket) binder {
	return binder{sockets: s}
}

type binder struct {
	EnableDHT bool
	sockets   []sockets.Socket
}

func (t binder) Options(opts ...BinderOption) binder {
	return langx.Clone(t, opts...)
}

// Bind the client to available networks. consumes the result of NewClient.
func (t binder) Bind(cl *Client, err error) (*Client, error) {
	if err != nil {
		return nil, err
	}

	if len(t.sockets) == 0 {
		cl.Close()
		return nil, errorsx.Errorf("at least one socket is required")
	}

	for _, s := range t.sockets {
		if err = cl.Bind(s); err != nil {
			cl.Close()
			return nil, err
		}

		if t.EnableDHT {
			if err = cl.BindDHT(s); err != nil {
				cl.Close()
				return nil, err
			}
		}
	}

	return cl, nil
}

func (t binder) Close() (err error) {
	for _, s := range t.sockets {
		err = errors.Join(err, s.Close())
	}

	return err
}
