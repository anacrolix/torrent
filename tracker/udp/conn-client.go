package udp

import (
	"context"
	"net"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/missinggo/v2"
)

type NewConnClientOpts struct {
	// The network to operate to use, such as "udp4", "udp", "udp6".
	Network string
	// Tracker address
	Host string
	// If non-nil, forces either IPv4 or IPv6 in the UDP tracker wire protocol.
	Ipv6 *bool
}

// Manages a Client with a specific connection.
type ConnClient struct {
	Client  Client
	conn    net.Conn
	d       Dispatcher
	readErr error
	ipv6    bool
}

func (cc *ConnClient) reader() {
	b := make([]byte, 0x800)
	for {
		n, err := cc.conn.Read(b)
		if err != nil {
			// TODO: Do bad things to the dispatcher, and incoming calls to the client if we have a
			// read error.
			cc.readErr = err
			break
		}
		_ = cc.d.Dispatch(b[:n])
		// if err != nil {
		// 	log.Printf("dispatching packet received on %v (%q): %v", cc.conn, string(b[:n]), err)
		// }
	}
}

func ipv6(opt *bool, network string, conn net.Conn) bool {
	if opt != nil {
		return *opt
	}
	switch network {
	case "udp4":
		return false
	case "udp6":
		return true
	}
	rip := missinggo.AddrIP(conn.RemoteAddr())
	return rip.To16() != nil && rip.To4() == nil
}

func NewConnClient(opts NewConnClientOpts) (cc *ConnClient, err error) {
	conn, err := net.Dial(opts.Network, opts.Host)
	if err != nil {
		return
	}
	cc = &ConnClient{
		Client: Client{
			Writer: conn,
		},
		conn: conn,
		ipv6: ipv6(opts.Ipv6, opts.Network, conn),
	}
	cc.Client.Dispatcher = &cc.d
	go cc.reader()
	return
}

func (c *ConnClient) Close() error {
	return c.conn.Close()
}

func (c *ConnClient) Announce(
	ctx context.Context, req AnnounceRequest, opts Options,
) (
	h AnnounceResponseHeader, nas AnnounceResponsePeers, err error,
) {
	nas = func() AnnounceResponsePeers {
		if c.ipv6 {
			return &krpc.CompactIPv6NodeAddrs{}
		} else {
			return &krpc.CompactIPv4NodeAddrs{}
		}
	}()
	h, err = c.Client.Announce(ctx, req, nas, opts)
	return
}
