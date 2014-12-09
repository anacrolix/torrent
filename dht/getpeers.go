package dht

import (
	"log"
	"net"
	"sync"

	"bitbucket.org/anacrolix/go.torrent/util"
)

type peerDiscovery struct {
	*peerStream
	triedAddrs map[string]struct{}
	backlog    map[string]net.Addr
	pending    int
	server     *Server
	infoHash   string
}

const (
	parallelQueries = 100
	backlogMaxLen   = 10000
)

func (me *peerDiscovery) Close() {
	me.peerStream.Close()
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
		addr, err := bootstrapAddr()
		if err != nil {
			return nil, err
		}
		startAddrs = append(startAddrs, addr)
	}
	disc := &peerDiscovery{
		peerStream: &peerStream{
			Values: make(chan peerStreamValue),
			stop:   make(chan struct{}),
			values: make(chan peerStreamValue),
		},
		triedAddrs: make(map[string]struct{}, 500),
		backlog:    make(map[string]net.Addr, parallelQueries),
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
	disc.mu.Lock()
	for _, addr := range startAddrs {
		disc.contact(addr)
	}
	disc.mu.Unlock()
	return disc.peerStream, nil
}

func (me *peerDiscovery) gotNodeAddr(addr net.Addr) {
	if util.AddrPort(addr) == 0 {
		// Not a contactable address.
		return
	}
	if me.server.ipBlocked(util.AddrIP(addr)) {
		return
	}
	if _, ok := me.triedAddrs[addr.String()]; ok {
		return
	}
	if _, ok := me.backlog[addr.String()]; ok {
		return
	}
	if me.pending >= parallelQueries {
		if len(me.backlog) < backlogMaxLen {
			me.backlog[addr.String()] = addr
		}
	} else {
		me.contact(addr)
	}
}

func (me *peerDiscovery) contact(addr net.Addr) {
	me.triedAddrs[addr.String()] = struct{}{}
	if err := me.getPeers(addr); err != nil {
		log.Printf("error sending get_peers request to %s: %s", addr, err)
		return
	}
	me.pending++
}

func (me *peerDiscovery) transactionClosed() {
	me.pending--
	for key, addr := range me.backlog {
		if me.pending >= parallelQueries {
			break
		}
		delete(me.backlog, key)
		me.contact(addr)
	}
	if me.pending == 0 {
		me.Close()
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
			if nodes := m.Nodes(); len(nodes) != 0 {
				for _, n := range nodes {
					me.responseNode(n)
				}
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
	select {
	case <-ps.stop:
	default:
		close(ps.stop)
	}
}
