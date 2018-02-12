package tracker

import (
	"net"
)

type Peer struct {
	IP   net.IP
	Port int
	ID   []byte
}

// Set from the non-compact form in BEP 3.
func (p *Peer) fromDictInterface(d map[string]interface{}) {
	p.IP = net.ParseIP(d["ip"].(string))
	p.ID = []byte(d["peer id"].(string))
	p.Port = int(d["port"].(int64))
}
