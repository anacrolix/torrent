package tracker

import (
	"net"

	"github.com/anacrolix/dht/v2/krpc"
)

type Peer struct {
	IP   net.IP
	Port int
	ID   []byte
}

// Set from the non-compact form in BEP 3.
func (p *Peer) FromDictInterface(d map[string]interface{}) {
	p.IP = net.ParseIP(d["ip"].(string))
	if _, ok := d["peer id"]; ok {
		p.ID = []byte(d["peer id"].(string))
	}
	p.Port = int(d["port"].(int64))
}

func (p Peer) FromNodeAddr(na krpc.NodeAddr) Peer {
	p.IP = na.IP
	p.Port = na.Port
	return p
}
