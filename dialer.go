package torrent

import (
	"context"
	"net"

	"github.com/anacrolix/missinggo/perf"
)

type Dialer interface {
	Dial(_ context.Context, addr string) (net.Conn, error)
	DialerNetwork() string
}

type NetDialer struct {
	Network string
	Dialer  net.Dialer
}

func (me NetDialer) DialerNetwork() string {
	return me.Network
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
