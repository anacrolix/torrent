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

func listenAll(networks []string, addr string) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	for {
		ss, retry, err := listenAllRetry(networks, addr)
		if !retry {
			return ss, err
		}
	}
}

func listenAllRetry(networks []string, addr string) (ss []socket, retry bool, err error) {
	_, port, err := missinggo.ParseHostPort(addr)
	if err != nil {
		err = fmt.Errorf("error parsing addr: %s", err)
		return
	}
	ss = make([]socket, 1, len(networks))
	ss[0], err = listen(networks[0], addr)
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
	restAddr := setPort(addr, missinggo.AddrPort(ss[0].Addr()))
	for _, n := range networks[1:] {
		s, err := listen(n, restAddr)
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
