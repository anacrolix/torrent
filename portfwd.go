package torrent

import (
	"context"
	"iter"
	"log"
	"net/netip"
	"time"

	alog "github.com/anacrolix/log"
	"github.com/anacrolix/upnp"
	"github.com/james-lawrence/torrent/internal/errorsx"
)

func addPortMapping(d upnp.Device, proto upnp.Protocol, internalPort int, upnpID string) (_zero netip.AddrPort, err error) {
	ip, err := d.GetExternalIPAddress()
	if err != nil {
		return _zero, errorsx.Wrapf(err, "error adding %s port mapping unable to determined external ip", proto)
	}

	externalPort, err := d.AddPortMapping(proto, internalPort, internalPort, upnpID, 0)
	if err != nil {
		return _zero, errorsx.Wrapf(err, "error adding %s port mapping", proto)
		// } else if externalPort != internalPort {
		// 	cl.config.warn().Printf("external port %d does not match internal port %d in port mapping\n", externalPort, internalPort)
		// } else {
		// 	cl.config.debug().Printf("forwarded external %s port %d\n", proto, externalPort)
	}

	return netip.AddrPortFrom(netip.AddrFrom16([16]byte(ip.To16())), uint16(externalPort)), nil
}

func (cl *Client) forwardPort() {
	if cl.config.dynamicip == nil {
		return
	}

	addrs, err := cl.config.dynamicip(context.Background(), cl)
	if err != nil {
		cl.config.errors().Println(err)
		return
	}

	for addrport := range addrs {
		log.Println("dynamic ip update", cl.LocalPort(), "->", addrport)
	}
}

func UPnPDynamicIP(ctx context.Context, c *Client) (iter.Seq[netip.AddrPort], error) {
	return func(yield func(netip.AddrPort) bool) {
		ds := upnp.Discover(0, 2*time.Second, alog.Default.WithValues(c).WithValues("upnp-discover"))
		c.config.debug().Printf("discovered %d upnp devices\n", len(ds))
		c.lock()
		port := c.LocalPort()
		id := c.config.UpnpID
		c.unlock()

		for _, d := range ds {
			if c, err := addPortMapping(d, upnp.TCP, port, id); err == nil {
				if !yield(c) {
					return
				}
			} else {
				log.Println("unable to map port", err)
			}

			if c, err := addPortMapping(d, upnp.UDP, port, id); err == nil {
				if !yield(c) {
					return
				}
			} else {
				log.Println("unable to map port", err)
			}
		}
	}, nil
}
