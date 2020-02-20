package torrent

import (
	"context"
	"net"
	"strconv"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/perf"
	"github.com/pkg/errors"
)

type Listener interface {
	net.Listener
}

type socket interface {
	Listener
	Dialer
}

func listen(n network, addr string, f firewallCallback) (socket, error) {
	switch {
	case n.Tcp:
		return listenTcp(n.String(), addr)
	case n.Udp:
		return listenUtp(n.String(), addr, f)
	default:
		panic(n)
	}
}

func listenTcp(network, address string) (s socket, err error) {
	l, err := net.Listen(network, address)
	return tcpSocket{
		Listener: l,
		NetDialer: NetDialer{
			Network: network,
		},
	}, err
}

type tcpSocket struct {
	net.Listener
	NetDialer
}

func listenAll(networks []network, getHost func(string) string, port int, f firewallCallback) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n.String())})
	}
	for {
		ss, retry, err := listenAllRetry(nahs, port, f)
		if !retry {
			return ss, err
		}
	}
}

type networkAndHost struct {
	Network network
	Host    string
}

func listenAllRetry(nahs []networkAndHost, port int, f firewallCallback) (ss []socket, retry bool, err error) {
	ss = make([]socket, 1, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	ss[0], err = listen(nahs[0].Network, net.JoinHostPort(nahs[0].Host, portStr), f)
	if err != nil {
		return nil, false, errors.Wrap(err, "first listen")
	}
	defer func() {
		if err != nil || retry {
			for _, s := range ss {
				s.Close()
			}
			ss = nil
		}
	}()
	portStr = strconv.FormatInt(int64(missinggo.AddrPort(ss[0].Addr())), 10)
	for _, nah := range nahs[1:] {
		s, err := listen(nah.Network, net.JoinHostPort(nah.Host, portStr), f)
		if err != nil {
			return ss,
				missinggo.IsAddrInUse(err) && port == 0,
				errors.Wrap(err, "subsequent listen")
		}
		ss = append(ss, s)
	}
	return
}

type firewallCallback func(net.Addr) bool

func listenUtp(network, addr string, fc firewallCallback) (socket, error) {
	us, err := NewUtpSocket(network, addr, fc)
	return utpSocketSocket{us, network}, err
}

type utpSocketSocket struct {
	utpSocket
	network string
}

func (me utpSocketSocket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	defer perf.ScopeTimerErr(&err)()
	return me.utpSocket.DialContext(ctx, me.network, addr)
}
