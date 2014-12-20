package dht

import (
	"bitbucket.org/anacrolix/go.torrent/util"
	"bitbucket.org/anacrolix/sync"
	"github.com/willf/bloom"
	"log"
	"net"
	"time"
)

type peerDiscovery struct {
	*peerStream
	triedAddrs *bloom.BloomFilter
	pending    int
	server     *Server
	infoHash   string
}

func (s *Server) GetPeers(infoHash string) (*peerStream, error) {
	s.mu.Lock()
	startAddrs := func() (ret []net.Addr) {
		for _, n := range s.closestGoodNodes(160, infoHash) {
			ret = append(ret, n.addr)
		}
		return
	}()
	s.mu.Unlock()
	if len(startAddrs) == 0 {
		addrs, err := bootstrapAddrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			startAddrs = append(startAddrs, addr)
		}
	}
	disc := &peerDiscovery{
		peerStream: &peerStream{
			Values: make(chan peerStreamValue),
			stop:   make(chan struct{}),
			values: make(chan peerStreamValue),
		},
		triedAddrs: bloom.NewWithEstimates(500000, 0.01),
		server:     s,
		infoHash:   infoHash,
	}
	// Function ferries from values to Values until discovery is halted.
	go func() {
		defer close(disc.Values)
		for {
			select {
			case psv := <-disc.values:
				select {
				case disc.Values <- psv:
				case <-disc.stop:
					return
				}
			case <-disc.stop:
				return
			}
		}
	}()
	for i, addr := range startAddrs {
		if i != 0 {
			time.Sleep(time.Millisecond)
		}
		disc.mu.Lock()
		disc.contact(addr)
		disc.mu.Unlock()
	}
	return disc.peerStream, nil
}

func (me *peerDiscovery) gotNodeAddr(addr net.Addr) {
	if util.AddrPort(addr) == 0 {
		// Not a contactable address.
		return
	}
	if me.triedAddrs.Test([]byte(addr.String())) {
		return
	}
	if me.server.ipBlocked(util.AddrIP(addr)) {
		return
	}
	me.contact(addr)
}

func (me *peerDiscovery) contact(addr net.Addr) {
	me.triedAddrs.Add([]byte(addr.String()))
	if err := me.getPeers(addr); err != nil {
		log.Printf("error sending get_peers request to %s: %s", addr, err)
		return
	}
	me.pending++
}

func (me *peerDiscovery) transactionClosed() {
	me.pending--
	if me.pending == 0 {
		me.close()
		return
	}
}

func (me *peerDiscovery) responseNode(node NodeInfo) {
	me.gotNodeAddr(node.Addr)
}

func (me *peerDiscovery) closingCh() chan struct{} {
	return me.peerStream.stop
}

func (me *peerDiscovery) getPeers(addr net.Addr) error {
	me.server.mu.Lock()
	defer me.server.mu.Unlock()
	t, err := me.server.getPeers(addr, me.infoHash)
	if err != nil {
		return err
	}
	go func() {
		select {
		case m := <-t.Response:
			me.mu.Lock()
			for _, n := range m.Nodes() {
				me.responseNode(n)
			}
			me.mu.Unlock()
			if vs := extractValues(m); vs != nil {
				nodeInfo := NodeInfo{
					Addr: t.remoteAddr,
				}
				id := func() string {
					defer func() {
						recover()
					}()
					return m["r"].(map[string]interface{})["id"].(string)
				}()
				copy(nodeInfo.ID[:], id)
				select {
				case me.peerStream.values <- peerStreamValue{
					Peers:    vs,
					NodeInfo: nodeInfo,
				}:
				case <-me.peerStream.stop:
				}
			}
		case <-me.closingCh():
		}
		t.Close()
		me.mu.Lock()
		me.transactionClosed()
		me.mu.Unlock()
	}()
	return nil
}

type peerStreamValue struct {
	Peers    []util.CompactPeer // Peers given in get_peers response.
	NodeInfo                    // The node that gave the response.
}

type peerStream struct {
	mu     sync.Mutex
	Values chan peerStreamValue
	// Inner chan is set to nil when on close.
	values chan peerStreamValue
	stop   chan struct{}
}

func (ps *peerStream) Close() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.close()
}

func (ps *peerStream) close() {
	select {
	case <-ps.stop:
	default:
		close(ps.stop)
	}
}
