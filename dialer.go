package torrent

import (
	"context"
	"net"

	"github.com/anacrolix/missinggo/perf"
)

type Dialer interface {
	// The network is implied by the instance.
	Dial(_ context.Context, addr string) (net.Conn, error)
	// This is required for registering with the connection tracker (router connection table
	// emulating rate-limiter) before dialing. TODO: What about connections that wouldn't infringe
	// on routers, like localhost or unix sockets.
	LocalAddr() net.Addr
}

type NetDialer struct {
	Network string
	Dialer  net.Dialer
}

func (me NetDialer) Dial(ctx context.Context, addr string) (_ net.Conn, err error) {
	defer perf.ScopeTimerErr(&err)()
	return me.Dialer.DialContext(ctx, me.Network, addr)
}

func (me NetDialer) LocalAddr() net.Addr {
	return netDialerLocalAddr{me.Network, me.Dialer.LocalAddr}
}

type netDialerLocalAddr struct {
	network string
	addr    net.Addr
}

func (me netDialerLocalAddr) Network() string { return me.network }

func (me netDialerLocalAddr) String() string {
	if me.addr == nil {
		return ""
	}
	return me.addr.String()
}
