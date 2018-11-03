package torrent

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/perf"
	"golang.org/x/net/proxy"
)

type dialer interface {
	dial(_ context.Context, addr string) (net.Conn, error)
}

type socket interface {
	net.Listener
	dialer
}

type proxyConn struct {
	net.Conn
	remoteAddr net.Addr
}

// RemoteAddr returns real RemoteAddr of a proxied connection,
// instead of the first hop's RemoteAddr, which is always the proxy address.
func (c *proxyConn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}

	return c.Conn.RemoteAddr()
}

func getNetworkDialer(network string, dialer proxy.Dialer) func(ctx context.Context, network string, addr string) (net.Conn, error) {
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		conn, err := dialer.Dial(network, addr)
		if err != nil {
			return nil, err
		}

		// When 'network' is tcp6/udp6 and address is tcp4 - we get resolve exception.
		// As a workaround - we can run same resolve with tcp4/udp4, instead of tcp6/udp6.
		// Currently we leave old RemoteAddr().
		if len(network) > 3 {
			network = network[:3]
		}
		if strings.Contains(network, "tcp") {
			if remoteAddr, err := net.ResolveTCPAddr(network, addr); err == nil {
				return &proxyConn{conn, remoteAddr}, nil
			}
		} else {
			if remoteAddr, err := net.ResolveUDPAddr(network, addr); err == nil {
				return &proxyConn{conn, remoteAddr}, nil
			}
		}

		return conn, nil
	}
}

func getProxyDialer(proxyURL string) (proxy.Dialer, error) {
	fixedURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	return proxy.FromURL(fixedURL, proxy.Direct)
}

func listen(network, addr, proxyURL string, f firewallCallback) (socket, error) {
	if isTcpNetwork(network) {
		return listenTcp(network, addr, proxyURL)
	} else if isUtpNetwork(network) {
		return listenUtp(network, addr, proxyURL, f)
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

func listenTcp(network, address, proxyURL string) (s socket, err error) {
	l, err := net.Listen(network, address)
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			l.Close()
		}
	}()

	// If we don't need the proxy - then we should return default net.Dialer,
	// otherwise, let's try to parse the proxyURL and return proxy.Dialer
	if len(proxyURL) != 0 {
		if dialer, err := getProxyDialer(proxyURL); err == nil {
			return tcpSocket{l, &net.Dialer{}, network, getNetworkDialer(network, dialer)}, nil
		}
	}

	return tcpSocket{l, &net.Dialer{}, network, nil}, nil
}

type tcpSocket struct {
	net.Listener
	*net.Dialer
	network string
	d       func(ctx context.Context, network string, addr string) (net.Conn, error)
}

func (me tcpSocket) dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	defer perf.ScopeTimerErr(&err)()

	if me.d != nil {
		return me.d(ctx, me.network, addr)
	}

	return me.DialContext(ctx, me.network, addr)
}

func setPort(addr string, port int) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		panic(err)
	}
	return net.JoinHostPort(host, strconv.FormatInt(int64(port), 10))
}

func listenAll(networks []string, getHost func(string) string, port int, proxyURL string, f firewallCallback) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n)})
	}
	for {
		ss, retry, err := listenAllRetry(nahs, port, proxyURL, f)
		if !retry {
			return ss, err
		}
	}
}

type networkAndHost struct {
	Network string
	Host    string
}

func listenAllRetry(nahs []networkAndHost, port int, proxyURL string, f firewallCallback) (ss []socket, retry bool, err error) {
	ss = make([]socket, 1, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	ss[0], err = listen(nahs[0].Network, net.JoinHostPort(nahs[0].Host, portStr), proxyURL, f)
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
		s, err := listen(nah.Network, net.JoinHostPort(nah.Host, portStr), proxyURL, f)
		if err != nil {
			return ss,
				missinggo.IsAddrInUse(err) && port == 0,
				fmt.Errorf("subsequent listen: %s", err)
		}
		ss = append(ss, s)
	}
	return
}

type firewallCallback func(net.Addr) bool

func listenUtp(network, addr, proxyURL string, fc firewallCallback) (s socket, err error) {
	us, err := NewUtpSocket(network, addr, fc)
	if err != nil {
		return
	}

	if len(proxyURL) != 0 {
		if dialer, err := getProxyDialer(proxyURL); err == nil {
			return utpSocketSocket{us, network, getNetworkDialer(network, dialer)}, nil
		}
	}

	return utpSocketSocket{us, network, nil}, nil
}

type utpSocketSocket struct {
	utpSocket
	network string
	d       func(ctx context.Context, network string, addr string) (net.Conn, error)
}

func (me utpSocketSocket) dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	defer perf.ScopeTimerErr(&err)()

	if me.d != nil {
		return me.d(ctx, me.network, addr)
	}

	return me.utpSocket.DialContext(ctx, me.network, addr)
}
