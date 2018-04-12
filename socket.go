package torrent

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/anacrolix/missinggo"
)

type dialer interface {
	dial(_ context.Context, addr string) (net.Conn, error)
}

type socket interface {
	net.Listener
	dialer
}

func listen(network, addr string) (socket, error) {
	if isTcpNetwork(network) {
		return listenTcp(network, addr)
	} else if isUtpNetwork(network) {
		return listenUtp(network, addr)
	} else {
		panic(fmt.Sprintf("unknown network %q", network))
	}
}

func isTcpNetwork(s string) bool {
	return strings.Contains(s, "tcp")
}

func isUtpNetwork(s string) bool {
	return strings.Contains(s, "utp") || strings.Contains(s, "udp")
}

func listenTcp(network, address string) (s socket, err error) {
	l, err := net.Listen(network, address)
	if err != nil {
		return
	}
	return tcpSocket{l}, nil
}

type tcpSocket struct {
	net.Listener
}

func (me tcpSocket) dial(ctx context.Context, addr string) (net.Conn, error) {
	return net.Dial(me.Addr().Network(), addr)
}

func setPort(addr string, port int) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	return net.JoinHostPort(host, strconv.FormatInt(int64(port), 10))
}

func listenAll(networks []string, getHost func(string) string, port int) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n)})
	}
	for {
		ss, retry, err := listenAllRetry(nahs, port)
		if !retry {
			return ss, err
		}
	}
}

type networkAndHost struct {
	Network string
	Host    string
}

func listenAllRetry(nahs []networkAndHost, port int) (ss []socket, retry bool, err error) {
	ss = make([]socket, 1, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	ss[0], err = listen(nahs[0].Network, net.JoinHostPort(nahs[0].Host, portStr))
	if err != nil {
		return nil, false, fmt.Errorf("first listen: %s", err)
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
		s, err := listen(nah.Network, net.JoinHostPort(nah.Host, portStr))
		if err != nil {
			return ss,
				missinggo.IsAddrInUse(err) && port == 0,
				fmt.Errorf("subsequent listen: %s", err)
		}
		ss = append(ss, s)
	}
	return
}

func listenUtp(network, addr string) (s socket, err error) {
	us, err := NewUtpSocket(network, addr)
	if err != nil {
		return
	}
	return utpSocketSocket{us, network}, nil
}

type utpSocketSocket struct {
	utpSocket
	network string
}

func (me utpSocketSocket) dial(ctx context.Context, addr string) (net.Conn, error) {
	return me.utpSocket.DialContext(ctx, me.network, addr)
}
