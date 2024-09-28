package torrent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
)

type Listener interface {
	// Accept waits for and returns the next connection to the listener.
	Accept() (net.Conn, error)

	// Addr returns the listener's network address.
	Addr() net.Addr
}

type socket interface {
	Listener
	Dialer
	Close() error
}

func listen(n network, addr string, f firewallCallback, logger log.Logger) (socket, error) {
	switch {
	case n.Tcp:
		return listenTcp(n.String(), addr)
	case n.Udp:
		return listenUtp(n.String(), addr, f, logger)
	default:
		panic(n)
	}
}

// Dialing TCP from a local port limits us to a single outgoing TCP connection to each remote
// client. Instead, this should be a last resort if we need to use holepunching, and only then to
// connect to other clients that actually try to holepunch TCP.
const dialTcpFromListenPort = false

var tcpListenConfig = net.ListenConfig{
	Control: func(network, address string, c syscall.RawConn) (err error) {
		controlErr := c.Control(func(fd uintptr) {
			if dialTcpFromListenPort {
				err = setReusePortSockOpts(fd)
			}
		})
		if err != nil {
			return
		}
		err = controlErr
		return
	},
	// BitTorrent connections manage their own keep-alives.
	KeepAlive: -1,
}

func listenTcp(network, address string) (s socket, err error) {
	l, err := tcpListenConfig.Listen(context.Background(), network, address)
	if err != nil {
		return
	}
	netDialer := net.Dialer{
		// We don't want fallback, as we explicitly manage the IPv4/IPv6 distinction ourselves,
		// although it's probably not triggered as I think the network is already constrained to
		// tcp4 or tcp6 at this point.
		FallbackDelay: -1,
		// BitTorrent connections manage their own keepalives.
		KeepAlive: tcpListenConfig.KeepAlive,
		Control: func(network, address string, c syscall.RawConn) (err error) {
			controlErr := c.Control(func(fd uintptr) {
				err = setSockNoLinger(fd)
				if err != nil {
					// Failing to disable linger is undesirable, but not fatal.
					log.Levelf(log.Debug, "error setting linger socket option on tcp socket: %v", err)
					err = nil
				}
				// This is no longer required I think, see
				// https://github.com/anacrolix/torrent/discussions/856. I added this originally to
				// allow dialling out from the client's listen port, but that doesn't really work. I
				// think Linux older than ~2013 doesn't support SO_REUSEPORT.
				if dialTcpFromListenPort {
					err = setReusePortSockOpts(fd)
				}
			})
			if err == nil {
				err = controlErr
			}
			return
		},
	}
	if dialTcpFromListenPort {
		netDialer.LocalAddr = l.Addr()
	}
	s = tcpSocket{
		Listener: l,
		NetworkDialer: NetworkDialer{
			Network: network,
			Dialer:  &netDialer,
		},
	}
	return
}

type tcpSocket struct {
	net.Listener
	NetworkDialer
}

func listenAll(
	networks []network,
	getHost func(string) string,
	port int,
	f firewallCallback,
	logger log.Logger,
) ([]socket, error) {
	if len(networks) == 0 {
		return nil, nil
	}
	var nahs []networkAndHost
	for _, n := range networks {
		nahs = append(nahs, networkAndHost{n, getHost(n.String())})
	}
	for {
		ss, retry, err := listenAllRetry(nahs, port, f, logger)
		if !retry {
			return ss, err
		}
	}
}

type networkAndHost struct {
	Network network
	Host    string
}

func isUnsupportedNetworkError(err error) bool {
	var sysErr *os.SyscallError
	//spewCfg := spew.NewDefaultConfig()
	//spewCfg.ContinueOnMethod = true
	//spewCfg.Dump(err)
	if !errors.As(err, &sysErr) {
		return false
	}
	//spewCfg.Dump(sysErr)
	//spewCfg.Dump(sysErr.Err.Error())
	// This might only be Linux specific.
	return sysErr.Syscall == "bind" && sysErr.Err.Error() == "cannot assign requested address"
}

func listenAllRetry(
	nahs []networkAndHost,
	port int,
	f firewallCallback,
	logger log.Logger,
) (ss []socket, retry bool, err error) {
	// Close all sockets on error or retry.
	defer func() {
		if err != nil || retry {
			for _, s := range ss {
				s.Close()
			}
			ss = nil
		}
	}()
	g.MakeSliceWithCap(&ss, len(nahs))
	portStr := strconv.FormatInt(int64(port), 10)
	for _, nah := range nahs {
		var s socket
		s, err = listen(nah.Network, net.JoinHostPort(nah.Host, portStr), f, logger)
		if err != nil {
			if isUnsupportedNetworkError(err) {
				err = nil
				continue
			}
			if len(ss) == 0 {
				// First relative to a possibly dynamic port (0).
				err = fmt.Errorf("first listen: %w", err)
			} else {
				err = fmt.Errorf("subsequent listen: %w", err)
			}
			retry = missinggo.IsAddrInUse(err) && port == 0
			return
		}
		ss = append(ss, s)
		portStr = strconv.FormatInt(int64(missinggo.AddrPort(ss[0].Addr())), 10)
	}
	return
}

// This isn't aliased from go-libutp since that assumes CGO.
type firewallCallback func(net.Addr) bool

func listenUtp(network, addr string, fc firewallCallback, logger log.Logger) (socket, error) {
	us, err := NewUtpSocket(network, addr, fc, logger)
	return utpSocketSocket{us, network}, err
}

// utpSocket wrapper, additionally wrapped for the torrent package's socket interface.
type utpSocketSocket struct {
	utpSocket
	network string
}

func (me utpSocketSocket) DialerNetwork() string {
	return me.network
}

func (me utpSocketSocket) Dial(ctx context.Context, addr string) (conn net.Conn, err error) {
	return me.utpSocket.DialContext(ctx, me.network, addr)
}
