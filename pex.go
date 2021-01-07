package torrent

import (
	"sync"
	"time"

	"github.com/james-lawrence/torrent/dht/v2/krpc"
	pp "github.com/james-lawrence/torrent/btprotocol"
)

func newPex() *pex {
	return &pex{
		m:     &sync.RWMutex{},
		conns: make(map[*connection]time.Time, 25),
	}
}

// peer exchange - http://bittorrent.org/beps/bep_0011.html
type pex struct {
	m     *sync.RWMutex
	conns map[*connection]time.Time
}

func (t *pex) snapshot() *pp.PexMsg {
	t.m.RLock()
	defer t.m.RUnlock()

	if len(t.conns) == 0 {
		return nil
	}

	tx := &pp.PexMsg{}
	nodeAddr := func(addr IpPort) krpc.NodeAddr {
		ip := addr.IP
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		return krpc.NodeAddr{IP: ip, Port: int(addr.Port)}
	}

	n := 0
	for c := range t.conns {
		if n > 25 {
			break
		}

		addr := c.remoteIPPort()
		f := c.pexPeerFlags()
		if c.ipv6() {
			tx.Added6 = append(tx.Added6, nodeAddr(addr))
			tx.Added6Flags = append(tx.Added6Flags, f)
		} else {
			tx.Added = append(tx.Added, nodeAddr(addr))
			tx.AddedFlags = append(tx.AddedFlags, f)
		}

		n++
	}

	return tx
}

func (t *pex) added(c *connection) {
	t.m.Lock()
	defer t.m.Unlock()
	t.conns[c] = time.Now()
}

func (t *pex) dropped(c *connection) {
	t.m.Lock()
	defer t.m.Unlock()
	delete(t.conns, c)
}
