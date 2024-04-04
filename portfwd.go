package torrent

import (
	"time"

	alog "github.com/anacrolix/log"
	"github.com/anacrolix/upnp"
	"github.com/pkg/errors"
)

func (cl *Client) addPortMapping(d upnp.Device, proto upnp.Protocol, internalPort int, upnpID string) {
	externalPort, err := d.AddPortMapping(proto, internalPort, internalPort, upnpID, 0)
	if err != nil {
		cl.config.warn().Println(errors.Wrapf(err, "error adding %s port mapping", proto))
	} else if externalPort != internalPort {
		cl.config.warn().Printf("external port %d does not match internal port %d in port mapping\n", externalPort, internalPort)
	} else {
		cl.config.debug().Printf("forwarded external %s port %d\n", proto, externalPort)
	}
}

func (cl *Client) forwardPort() {
	if cl.config.NoDefaultPortForwarding {
		return
	}

	ds := upnp.Discover(0, 2*time.Second, alog.Default.WithValues(cl).WithValues("upnp-discover"))
	cl.config.debug().Printf("discovered %d upnp devices\n", len(ds))
	cl.lock()
	port := cl.incomingPeerPort()
	id := cl.config.UpnpID
	cl.unlock()
	for _, d := range ds {
		go cl.addPortMapping(d, upnp.TCP, port, id)
		go cl.addPortMapping(d, upnp.UDP, port, id)
	}
}
