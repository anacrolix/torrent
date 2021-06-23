package tracker

import (
	"encoding/binary"
	"net/url"

	trHttp "github.com/anacrolix/torrent/tracker/http"
	"github.com/anacrolix/torrent/tracker/udp"
)

type udpAnnounce struct {
	url url.URL
	a   *Announce
}

func (c *udpAnnounce) Do(req AnnounceRequest) (res AnnounceResponse, err error) {
	cl, err := udp.NewConnClient(udp.NewConnClientOpts{
		Network: c.dialNetwork(),
		Host:    c.url.Host,
		Ipv6:    nil,
	})
	if err != nil {
		return
	}
	defer cl.Close()
	if req.IPAddress == 0 && c.a.ClientIp4.IP != nil {
		// I think we're taking bytes in big-endian order (all IPs), and writing it to a natively
		// ordered uint32. This will be correctly ordered when written back out by the UDP client
		// later. I'm ignoring the fact that IPv6 announces shouldn't have an IP address, we have a
		// perfectly good IPv4 address.
		req.IPAddress = binary.BigEndian.Uint32(c.a.ClientIp4.IP.To4())
	}
	h, nas, err := cl.Announce(c.a.Context, req, udp.Options{RequestUri: c.url.RequestURI()})
	if err != nil {
		return
	}
	res.Interval = h.Interval
	res.Leechers = h.Leechers
	res.Seeders = h.Seeders
	for _, cp := range nas.NodeAddrs() {
		res.Peers = append(res.Peers, trHttp.Peer{}.FromNodeAddr(cp))
	}
	return
}

func (c *udpAnnounce) dialNetwork() string {
	if c.a.UdpNetwork != "" {
		return c.a.UdpNetwork
	}
	return "udp"
}

// TODO: Split on IPv6, as BEP 15 says response peer decoding depends on network in use.
func announceUDP(opt Announce, _url *url.URL) (AnnounceResponse, error) {
	ua := udpAnnounce{
		url: *_url,
		a:   &opt,
	}
	return ua.Do(opt.Request)
}
