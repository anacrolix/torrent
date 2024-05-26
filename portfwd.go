package torrent

import (
	"fmt"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/upnp"
)

const UpnpDiscoverLogTag = "upnp-discover"

type upnpMapping struct {
	d            upnp.Device
	proto        upnp.Protocol
	externalPort int
}

func (cl *Client) addPortMapping(d upnp.Device, proto upnp.Protocol, internalPort int, upnpID string) {
	logger := cl.logger.WithContextText(fmt.Sprintf("UPnP device at %v: mapping internal %v port %v", d.GetLocalIPAddress(), proto, internalPort))
	externalPort, err := d.AddPortMapping(proto, internalPort, internalPort, upnpID, 0)
	if err != nil {
		logger.WithDefaultLevel(log.Warning).Printf("error: %v", err)
		return
	}
	cl.lock()
	cl.upnpMappings = append(cl.upnpMappings, &upnpMapping{d, proto, externalPort})
	cl.unlock()
	level := log.Info
	if externalPort != internalPort {
		level = log.Warning
	}
	logger.WithDefaultLevel(level).Printf("success: external port %v", externalPort)
}

func (cl *Client) forwardPort() {
	cl.lock()
	defer cl.unlock()
	if cl.config.NoDefaultPortForwarding {
		return
	}
	cl.unlock()
	ds := upnp.Discover(0, 2*time.Second, cl.logger.WithValues(UpnpDiscoverLogTag))
	cl.lock()
	cl.logger.WithDefaultLevel(log.Debug).Printf("discovered %d upnp devices", len(ds))
	port := cl.incomingPeerPort()
	id := cl.config.UpnpID
	cl.unlock()
	for _, d := range ds {
		go cl.addPortMapping(d, upnp.TCP, port, id)
		go cl.addPortMapping(d, upnp.UDP, port, id)
	}
	cl.lock()
}

func (cl *Client) deletePortMapping(d upnp.Device, proto upnp.Protocol, externalPort int) {
	logger := cl.logger.WithContextText(fmt.Sprintf("UPnP device at %v: delete mapping internal %v port %v", d.GetLocalIPAddress(), proto, externalPort))
	err := d.DeletePortMapping(proto, externalPort)
	if err != nil {
		logger.WithDefaultLevel(log.Warning).Printf("error: %v", err)
		return
	}
}

func (cl *Client) clearPortMappings() {
	mLen := len(cl.upnpMappings)
	if mLen == 0 {
		return
	}

	var wg sync.WaitGroup
	wg.Add(mLen)
	for _, m := range cl.upnpMappings {
		go func(m *upnpMapping) {
			defer wg.Done()
			cl.deletePortMapping(m.d, m.proto, m.externalPort)
		}(m)
	}
	cl.upnpMappings = nil
}
