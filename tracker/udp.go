package tracker

import (
	"encoding"
	"encoding/binary"
	"net"
	"net/url"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/missinggo/v2"
	trHttp "github.com/anacrolix/torrent/tracker/http"
	"github.com/anacrolix/torrent/tracker/udp"
)

type udpAnnounce struct {
	url url.URL
	a   *Announce
}

func (c *udpAnnounce) Close() error {
	return nil
}

func (c *udpAnnounce) ipv6(conn net.Conn) bool {
	if c.a.UdpNetwork == "udp6" {
		return true
	}
	rip := missinggo.AddrIP(conn.RemoteAddr())
	return rip.To16() != nil && rip.To4() == nil
}

func (c *udpAnnounce) Do(req AnnounceRequest) (res AnnounceResponse, err error) {
	conn, err := net.Dial(c.dialNetwork(), c.url.Host)
	if err != nil {
		return
	}
	defer conn.Close()
	if c.ipv6(conn) {
		// BEP 15
		req.IPAddress = 0
	} else if req.IPAddress == 0 && c.a.ClientIp4.IP != nil {
		req.IPAddress = binary.BigEndian.Uint32(c.a.ClientIp4.IP.To4())
	}
	d := udp.Dispatcher{}
	go func() {
		for {
			b := make([]byte, 0x800)
			n, err := conn.Read(b)
			if err != nil {
				break
			}
			d.Dispatch(b[:n])
		}
	}()
	cl := udp.Client{
		Dispatcher: &d,
		Writer:     conn,
	}
	nas := func() interface {
		encoding.BinaryUnmarshaler
		NodeAddrs() []krpc.NodeAddr
	} {
		if c.ipv6(conn) {
			return &krpc.CompactIPv6NodeAddrs{}
		} else {
			return &krpc.CompactIPv4NodeAddrs{}
		}
	}()
	h, err := cl.Announce(c.a.Context, req, nas, udp.Options{RequestUri: c.url.RequestURI()})
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
	defer ua.Close()
	return ua.Do(opt.Request)
}
