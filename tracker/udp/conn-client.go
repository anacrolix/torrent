package udp

import (
	"context"
	"log/slog"
	"net"

	"github.com/anacrolix/missinggo/v2"
)

type listenPacketFunc func(network, addr string) (net.PacketConn, error)

type NewConnClientOpts struct {
	// The network to operate to use, such as "udp4", "udp", "udp6". Ignored if Socket is set.
	Network string
	// Tracker address
	Host string
	// If non-nil, forces either IPv4 or IPv6 in the UDP tracker wire protocol.
	Ipv6 *bool
	// Logger to use for internal errors. Ignored if Socket is set.
	Logger *slog.Logger
	// Custom function to use as a substitute for net.ListenPacket. Ignored if Socket is set.
	ListenPacket listenPacketFunc
	// If non-nil, the client sends and receives through this shared Socket instead of opening
	// its own conn. Closing the ConnClient doesn't close the Socket.
	Socket *Socket
}

// Manages a Client with a specific connection.
type ConnClient struct {
	Client  Client
	socket  *Socket
	owned   bool
	newOpts NewConnClientOpts
}

func ipv6(opt *bool, network string, remoteAddr net.Addr) bool {
	if opt != nil {
		return *opt
	}
	switch network {
	case "udp4":
		return false
	case "udp6":
		return true
	}
	rip := missinggo.AddrIP(remoteAddr)
	return rip.To16() != nil && rip.To4() == nil
}

func NewConnClient(opts NewConnClientOpts) (cc *ConnClient, err error) {
	s := opts.Socket
	owned := s == nil
	if owned {
		s = &Socket{
			Network:      opts.Network,
			ListenPacket: opts.ListenPacket,
			Logger:       opts.Logger,
		}
		// Open the conn now, so errors are returned here as they were before Socket existed.
		_, err = s.getConn()
		if err != nil {
			return
		}
	}
	cc = &ConnClient{
		Client: Client{
			Writer: socketWriter{
				s:       s,
				address: opts.Host,
			},
			Dispatcher: &s.d,
		},
		socket:  s,
		owned:   owned,
		newOpts: opts,
	}
	return
}

func (cc *ConnClient) Close() error {
	if !cc.owned {
		return nil
	}
	return cc.socket.Close()
}

func (cc *ConnClient) Announce(
	ctx context.Context, req AnnounceRequest, opts Options,
) (
	h AnnounceResponseHeader, nas AnnounceResponsePeers, err error,
) {
	return cc.Client.Announce(ctx, req, opts, func(addr net.Addr) bool {
		return ipv6(cc.newOpts.Ipv6, cc.socket.network(), addr)
	})
}

// LocalAddr returns the local address of the client's conn, or nil if it isn't open.
func (cc *ConnClient) LocalAddr() net.Addr {
	return cc.socket.LocalAddr()
}
