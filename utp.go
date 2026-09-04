package torrent

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/anacrolix/go-libutp/utp"
	"github.com/anacrolix/log"
)

// Abstracts the utp Socket, so the implementation can be selected from
// different packages.
type utpSocket interface {
	net.PacketConn
	// net.Listener, but we can't have duplicate Close.
	Accept() (net.Conn, error)
	Addr() net.Addr
	// net.Dialer but there's no interface.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// Dial(addr string) (net.Conn, error)
}

// Applies the options and firewall callback shared by both utp socket constructors. Returns the
// socket as the local interface so a nil utp.Socket doesn't become a non-nil utpSocket.
func newUtpSocket(
	newSocket func(...utp.Option) (utp.Socket, error),
	fc firewallCallback,
	slogger *slog.Logger,
) (_ utpSocket, err error) {
	var opts []utp.Option
	if slogger != nil {
		opts = append(opts, utp.WithLogger(slogger))
	}
	s, err := newSocket(opts...)
	if s == nil {
		if err == nil {
			err = fmt.Errorf("creating %v socket: nil socket without error", utp.Default)
		}
		return
	}
	if err != nil {
		err = fmt.Errorf("creating %v socket: %w", utp.Default, err)
		return s, err
	}
	if fc != nil {
		s.SetFirewallCallback(fc)
	}
	return s, nil
}

func NewUtpSocketSlogger(network, addr string, fc firewallCallback, slogger *slog.Logger) (utpSocket, error) {
	return newUtpSocket(func(opts ...utp.Option) (utp.Socket, error) {
		return utp.NewSocket(network, addr, opts...)
	}, fc, slogger)
}

func NewUtpSocketFromPacketConn(pc net.PacketConn, fc firewallCallback, slogger *slog.Logger) (utpSocket, error) {
	return newUtpSocket(func(opts ...utp.Option) (utp.Socket, error) {
		return utp.NewSocketFromPacketConn(pc, opts...)
	}, fc, slogger)
}

// Deprecated: Use [NewUtpSocketSlogger].
func NewUtpSocket(network, addr string, fc firewallCallback, logger log.Logger) (utpSocket, error) {
	var sl *slog.Logger
	if !logger.IsZero() {
		sl = logger.Slogger()
	} else {
		sl = slog.Default()
	}
	return NewUtpSocketSlogger(network, addr, fc, sl)
}
