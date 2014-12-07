package dht

import (
	"log"
	"net"
	"sync"
	"time"

	"bitbucket.org/anacrolix/go.torrent/util"
)

type peerDiscovery struct {
	*peerStream
	triedAddrs        map[string]struct{}
	contactAddrs      chan net.Addr
	pending           int
	transactionClosed chan struct{}
	server            *Server
	infoHash          string
}

func (me *peerDiscovery) Close() {
	me.peerStream.Close()
	close(me.contactAddrs)
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
		},
		triedAddrs:        make(map[string]struct{}, 500),
		contactAddrs:      make(chan net.Addr),
		transactionClosed: make(chan struct{}),
		server:            s,
		infoHash:          infoHash,
	}
	go disc.loop()
	for i, addr := range startAddrs {
		if i != 0 {
			time.Sleep(time.Microsecond)
		}
		disc.contact(addr)
	}
	return disc.peerStream, nil
}

func (me *peerDiscovery) contact(addr net.Addr) {
	select {
	case me.contactAddrs <- addr:
	case <-me.closingCh():
	}
}

func (me *peerDiscovery) responseNode(node NodeInfo) {
	me.contact(node.Addr)
}

func (me *peerDiscovery) loop() {
	for {
		select {
		case addr := <-me.contactAddrs:
			if me.pending >= 160 {
				break
			}
			if _, ok := me.triedAddrs[addr.String()]; ok {
				break
			}
			me.triedAddrs[addr.String()] = struct{}{}
			if err := me.getPeers(addr); err != nil {
				log.Printf("error sending get_peers request to %s: %s", addr, err)
				break
			}
			// log.Printf("contacting %s", addr)
			me.pending++
		case <-me.transactionClosed:
			me.pending--
			// log.Printf("pending: %d", me.pending)
			if me.pending == 0 {
				me.Close()
				return
			}
		}
	}
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
			if nodes := m.Nodes(); len(nodes) != 0 {
				for _, n := range nodes {
					me.responseNode(n)
				}
			}
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
				case me.peerStream.Values <- peerStreamValue{
					Peers:    vs,
					NodeInfo: nodeInfo,
				}:
				case <-me.peerStream.stop:
				}
			}
		case <-me.closingCh():
		}
		t.Close()
		me.transactionClosed <- struct{}{}
	}()
	return nil
}

func (me *peerDiscovery) streamValue(psv peerStreamValue) {
	me.peerStream.Values <- psv
}

type peerStreamValue struct {
	Peers    []util.CompactPeer // Peers given in get_peers response.
	NodeInfo                    // The node that gave the response.
}

type peerStream struct {
	mu     sync.Mutex
	Values chan peerStreamValue
	stop   chan struct{}
}

func (ps *peerStream) Close() {
	ps.mu.Lock()
	select {
	case <-ps.stop:
	default:
		close(ps.stop)
		close(ps.Values)
	}
	ps.mu.Unlock()
}
