package torrent

import (
	"net"
	"strconv"
)

// NewDefaultClient setup a client and connect a using defaults settings.
func NewDefaultClient() (c *Client, err error) {
	return NewAutobind().Bind(NewClient(NewDefaultClientConfig()))
}

// NewAutobind used to automatically listen to available networks
// on the system. limited configuration. use client.Bind for more
// robust configuration.
func NewAutobind() Autobind {
	return Autobind{
		ListenHost: func(string) string { return "" },
		ListenPort: 0,
	}
}

// NewAutobindLoopback autobind to the loopback device.
func NewAutobindLoopback() Autobind {
	return Autobind{
		ListenHost: LoopbackListenHost,
		ListenPort: 0,
	}
}

// NewAutobindSpecified for use in testing only, panics if invalid host/port.
func NewAutobindSpecified(dst string) Autobind {
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

// Autobind manages automatically binding a client to available networks.
type Autobind struct {
	// The address to listen for new uTP and TCP bittorrent protocol
	// connections. DHT shares a UDP socket with uTP unless configured
	// otherwise.
	ListenHost func(network string) string
	ListenPort int
}

// Bind the client to available networks. consumes the result of NewClient.
func (t Autobind) Bind(cl *Client, err error) (*Client, error) {
	var (
		sockets []socket
	)

	if err != nil {
		return nil, err
	}

	if sockets, err = listenAll(t.listenNetworks(cl.config), t.ListenHost, t.ListenPort, cl.config.ProxyURL, cl.firewallCallback); err != nil {
		return nil, err
	}

	// Check for panics.
	cl.LocalPort()

	for _, s := range sockets {
		if peerNetworkEnabled(parseNetworkString(s.Addr().Network()), cl.config) {
			if err = cl.Bind(s); err != nil {
				cl.Close()
				return nil, err
			}
		} else if !cl.config.NoDHT {
			if err = cl.bindDHT(s); err != nil {
				cl.Close()
				return nil, err
			}
		}
	}

	return cl, nil
}

func (t Autobind) enabledPeerNetworks(c *ClientConfig) (ns []network) {
	for _, n := range allPeerNetworks {
		if peerNetworkEnabled(n, c) {
			ns = append(ns, n)
		}
	}
	return
}

func (t Autobind) listenNetworks(c *ClientConfig) (ns []network) {
	for _, n := range allPeerNetworks {
		if c.listenOnNetwork(n) {
			ns = append(ns, n)
		}
	}
	return
}
