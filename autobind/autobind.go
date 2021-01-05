// Package autobind for automically binding on the local server
// this package is only for convience and it's suggested to use
// torrent.NewSocketsBind instead.
package autobind

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/anacrolix/missinggo"
	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/internal/utpx"
	"github.com/james-lawrence/torrent/sockets"
	"github.com/pkg/errors"
	"golang.org/x/net/proxy"
)

// NewDefaultClient setup a client and connect a using defaults settings.
func NewDefaultClient() (c *torrent.Client, err error) {
	return New().Bind(torrent.NewClient(torrent.NewDefaultClientConfig()))
}

// Option for configuring autobind.
type Option func(*Autobind)

// DisableUTP disable UTP sockets
func DisableUTP(a *Autobind) {
	a.DisableUTP = true
}

// DisableTCP disable TCP sockets
func DisableTCP(a *Autobind) {
	a.DisableTCP = true
}

// DisableDHT disables DHT.
func DisableDHT(a *Autobind) {
	a.NoDHT = true
}

// Autobind manages automatically binding a client to available networks.
type Autobind struct {
	// The address to listen for new uTP and TCP bittorrent protocol
	// connections. DHT shares a UDP socket with uTP unless configured
	// otherwise.
	ListenHost  func(network string) string
	ListenPort  int
	DisableIPv4 bool
	DisableIPv6 bool
	DisableTCP  bool
	DisableUTP  bool
	NoDHT       bool
}

// New used to automatically listen to available networks
// on the system. limited configuration options. use client.Bind for more
// robust configuration.
func New(options ...Option) Autobind {
	autobind := Autobind{
		ListenHost: func(string) string { return "" },
		ListenPort: 0,
	}

	for _, opt := range options {
		opt(&autobind)
	}

	return autobind
}

// NewLoopback autobind to the loopback device.
func NewLoopback(options ...Option) Autobind {
	return New(func(a *Autobind) {
		a.ListenHost = func(network string) string {
			if strings.Contains(network, "4") {
				return "127.0.0.1"
			}
			return "::1"
		}
		a.ListenPort = 0
	})
}

// NewSpecified for use in testing only, panics if invalid host/port.
func NewSpecified(dst string) Autobind {
	var (
		err         error
		port        int
		host, _port string
	)

	if host, _port, err = net.SplitHostPort(dst); err != nil {
		panic(err)
	}

	if port, err = strconv.Atoi(_port); err != nil {
		panic(err)
	}

	return Autobind{
		ListenHost: func(string) string { return host },
		ListenPort: port,
	}
}

// Bind the client to available networks. consumes the result of NewClient.
func (t Autobind) Bind(cl *torrent.Client, err error) (*torrent.Client, error) {
	var (
		sockets []sockets.Socket
	)

	if err != nil {
		return nil, err
	}

	config := cl.Config()
	config.NoDHT = t.NoDHT // TODO: remove NoDHT from client config.

	if sockets, err = listenAll(t.listenNetworks(), t.ListenHost, t.ListenPort, config.ProxyURL); err != nil {
		return nil, err
	}

	// Check for panics.
	cl.LocalPort()

	for _, s := range sockets {
		if t.peerNetworkEnabled(parseNetworkString(s.Addr().Network())) {
			if err = cl.Bind(s); err != nil {
				cl.Close()
				return nil, err
			}
		}
	}

	return cl, nil
}

func (t Autobind) enabledPeerNetworks() (ns []network) {
	for _, n := range allPeerNetworks {
		if t.peerNetworkEnabled(n) {
			ns = append(ns, n)
		}
	}
	return
}

func (t Autobind) listenNetworks() (ns []network) {
	for _, n := range allPeerNetworks {
		if t.listenOnNetwork(n) {
			ns = append(ns, n)
		}
	}
	return
}

func (t Autobind) listenOnNetwork(n network) bool {
	if n.Ipv4 && t.DisableIPv4 {
		return false
	}

	if n.Ipv6 && t.DisableIPv6 {
		return false
	}

	if n.TCP && t.DisableTCP {
		return false
	}

	if n.UDP && t.DisableUTP && t.NoDHT {
		return false
	}

	return true
}

func (t Autobind) peerNetworkEnabled(n network) bool {
	if t.DisableUTP && n.UDP {
		return false
	}
	if t.DisableTCP && n.TCP {
		return false
	}
	if t.DisableIPv6 && n.Ipv6 {
		return false
	}
	if t.DisableIPv4 && n.Ipv4 {
		return false
	}
	return true
}

type firewallCallback func(net.Addr) bool

type dialer interface {
	Dial(ctx context.Context, addr string) (net.Conn, error)
}

func getProxyDialer(proxyURL string) (proxy.ContextDialer, error) {
	fixedURL, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	d, err := proxy.FromURL(fixedURL, proxy.Direct)
	if err != nil {
		return nil, err
	}

	return proxyContextDialer(d), nil
}

type disabledListener struct {
	net.Listener
}

func (dl disabledListener) Accept() (net.Conn, error) {
	return nil, fmt.Errorf("tcp listener disabled due to proxy")
}

func listenAll(networks []network, getHost func(string) string, port int, proxyURL string) ([]sockets.Socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n.String())})
	}

	for {
		ss, retry, err := listenAllRetry(nahs, port, proxyURL)
		if !retry {
			return ss, err
		}
	}
}

func listenAllRetry(nahs []networkAndHost, port int, proxyURL string) (ss []sockets.Socket, retry bool, err error) {
	ss = make([]sockets.Socket, 1, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	ss[0], err = listen(nahs[0].Network, net.JoinHostPort(nahs[0].Host, portStr), proxyURL)
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
		s, err := listen(nah.Network, net.JoinHostPort(nah.Host, portStr), proxyURL)
		if err != nil {
			return ss,
				missinggo.IsAddrInUse(err) && port == 0,
				errors.Wrap(err, "subsequent listen")
		}
		ss = append(ss, s)
	}
	return
}

func listen(n network, addr, proxyURL string) (sockets.Socket, error) {
	switch {
	case n.TCP:
		return listenTCP(n.String(), addr, proxyURL)
	case n.UDP:
		return listenUtp(n.String(), addr, proxyURL)
	default:
		panic(n)
	}
}

func listenTCP(network, address, proxyURL string) (s sockets.Socket, err error) {
	l, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			l.Close()
		}
	}()

	// If we don't need the proxy - then we should return default net.Dialer,
	// otherwise, let's try to parse the proxyURL and return proxy.Dialer
	if len(proxyURL) != 0 {
		dl := disabledListener{l}
		dialer, err := getProxyDialer(proxyURL)
		if err != nil {
			return nil, err
		}

		return sockets.New(dl, dialer), nil
	}
	dialer := &net.Dialer{}
	return sockets.New(l, dialer), nil
}

func listenUtp(network, addr, proxyURL string) (s sockets.Socket, err error) {
	us, err := utpx.New(network, addr)
	if err != nil {
		return
	}

	// If we don't need the proxy - then we should return default net.Dialer,
	// otherwise, let's try to parse the proxyURL and return proxy.Dialer
	if len(proxyURL) != 0 {
		dialer, err := getProxyDialer(proxyURL)
		if err != nil {
			return nil, err
		}
		return sockets.New(disabledUtpSocket{us}, dialer), nil
	}

	dialer := &net.Dialer{}
	return sockets.New(us, dialer), nil
}

func proxyContextDialer(d proxy.Dialer) proxy.ContextDialer {
	if d, ok := d.(proxy.ContextDialer); ok {
		return d
	}

	return fakecontextdialer{d: d}
}

type fakecontextdialer struct {
	d proxy.Dialer
}

// WARNING: this can leak a goroutine for as long as the underlying Dialer implementation takes to timeout
// A Conn returned from a successful Dial after the context has been cancelled will be immediately closed.
func (t fakecontextdialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var (
		conn net.Conn
		done = make(chan struct{}, 1)
		err  error
	)
	go func() {
		conn, err = t.d.Dial(network, address)
		close(done)
		if conn != nil && ctx.Err() != nil {
			conn.Close()
		}
	}()
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case <-done:
	}
	return conn, err
}

type networkAndHost struct {
	Network network
	Host    string
}

type disabledUtpSocket struct {
	utpx.Socket
}

func (ds disabledUtpSocket) Accept() (net.Conn, error) {
	return nil, fmt.Errorf("utp listener disabled due to proxy")
}
