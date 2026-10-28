package dialer

import (
	"context"
	"net"

	"golang.org/x/net/proxy"
)

// Socks5 dials through a SOCKS5 proxy server (RFC 1928/1929).
type Socks5 struct {
	// Address of the SOCKS5 proxy, such as "127.0.0.1:1080".
	Addr string
	// Optional username/password credentials for the proxy.
	Auth *proxy.Auth
	// The dialer used to connect to the proxy itself. Defaults to
	// connecting directly.
	Forward T
}

var _ T = (*Socks5)(nil)

// NewSocks5 returns a dialer that routes connections through the SOCKS5 proxy
// at proxyAddr. An auth of nil connects without credentials.
func NewSocks5(proxyAddr string, auth *proxy.Auth) *Socks5 {
	return &Socks5{
		Addr: proxyAddr,
		Auth: auth,
	}
}

func (me *Socks5) DialerNetwork() string {
	return "tcp"
}

func (me *Socks5) Dial(ctx context.Context, addr string) (net.Conn, error) {
	network := me.DialerNetwork()
	d, err := proxy.SOCKS5(network, me.Addr, me.Auth, &proxyForwarder{me.Forward})
	if err != nil {
		return nil, err
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	return d.Dial(network, addr)
}

// Adapts a dialer.T to a proxy.Dialer for use as the forward dialer of a
// SOCKS5 proxy connection. A nil forward falls back to dialing the proxy
// directly.
type proxyForwarder struct {
	Forward T
}

var _ proxy.ContextDialer = (*proxyForwarder)(nil)

func (me *proxyForwarder) Dial(network, addr string) (net.Conn, error) {
	return me.DialContext(context.Background(), network, addr)
}

func (me *proxyForwarder) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if me.Forward != nil {
		return me.Forward.Dial(ctx, addr)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}
