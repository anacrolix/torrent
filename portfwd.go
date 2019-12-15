package torrent

import (
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/upnp"
)

func (cl *Client) addPortMapping(d upnp.Device, proto upnp.Protocol, internalPort int, upnpID string) {
	externalPort, err := d.AddPortMapping(proto, internalPort, internalPort, upnpID, 0)
	if err != nil {
		cl.logger.WithValues(log.Warning).Printf("error adding %s port mapping: %s", proto, err)
	} else if externalPort != internalPort {
		cl.logger.WithValues(log.Warning).Printf("external port %d does not match internal port %d in port mapping", externalPort, internalPort)
	} else {
		cl.logger.WithValues(log.Info).Printf("forwarded external %s port %d", proto, externalPort)
	}
}

func (cl *Client) forwardPort() {
	cl.lock()
	defer cl.unlock()
	if cl.config.NoDefaultPortForwarding {
		return
	}
	cl.unlock()
	ds := upnp.Discover(0, 2*time.Second, cl.logger.WithValues("upnp-discover"))
	cl.lock()
	cl.logger.Printf("discovered %d upnp devices", len(ds))
	port := cl.incomingPeerPort()
	id := cl.config.UpnpID
	cl.unlock()
	for _, d := range ds {
		go cl.addPortMapping(d, upnp.TCP, port, id)
		go cl.addPortMapping(d, upnp.UDP, port, id)
	}
	cl.lock()
}
