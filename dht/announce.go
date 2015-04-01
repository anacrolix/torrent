package dht

// get_peers and announce_peers.

import (
	"log"
	"time"

	"github.com/anacrolix/sync"
	"github.com/willf/bloom"

	"github.com/anacrolix/torrent/logonce"
	"github.com/anacrolix/torrent/util"
)

type peerDiscovery struct {
	*peerStream
	triedAddrs          *bloom.BloomFilter
	pending             int
	server              *Server
	infoHash            string
	numContacted        int
	announcePort        int
	announcePortImplied bool
}

func (pd *peerDiscovery) NumContacted() int {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	return pd.numContacted
}

func (s *Server) Announce(infoHash string, port int, impliedPort bool) (*peerDiscovery, error) {
	s.mu.Lock()
	startAddrs := func() (ret []dHTAddr) {
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
			startAddrs = append(startAddrs, newDHTAddr(addr))
		}
	}
	disc := &peerDiscovery{
		peerStream: &peerStream{
			Values: make(chan peerStreamValue, 100),
			stop:   make(chan struct{}),
			values: make(chan peerStreamValue),
		},
		triedAddrs:          bloom.NewWithEstimates(1000, 0.5),
		server:              s,
		infoHash:            infoHash,
		announcePort:        port,
		announcePortImplied: impliedPort,
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
	return disc, nil
}

func (me *peerDiscovery) gotNodeAddr(addr dHTAddr) {
	if util.AddrPort(addr) == 0 {
		// Not a contactable address.
		return
	}
	if me.triedAddrs.Test([]byte(addr.String())) {
		return
	}
	if me.server.ipBlocked(addr.UDPAddr().IP) {
		return
	}
	me.contact(addr)
}

func (me *peerDiscovery) contact(addr dHTAddr) {
	me.numContacted++
	me.triedAddrs.Add([]byte(addr.String()))
	if err := me.getPeers(addr); err != nil {
		log.Printf("error sending get_peers request to %s: %#v", addr, err)
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

func (me *peerDiscovery) announcePeer(to dHTAddr, token string) {
	me.server.mu.Lock()
	err := me.server.announcePeer(to, me.infoHash, me.announcePort, token, me.announcePortImplied)
	me.server.mu.Unlock()
	if err != nil {
		logonce.Stderr.Printf("error announcing peer: %s", err)
	}
}

func (me *peerDiscovery) getPeers(addr dHTAddr) error {
	me.server.mu.Lock()
	defer me.server.mu.Unlock()
	t, err := me.server.getPeers(addr, me.infoHash)
	if err != nil {
		return err
	}
	t.SetResponseHandler(func(m Msg) {
		// Register suggested nodes closer to the target info-hash.
		me.mu.Lock()
		for _, n := range m.Nodes() {
			me.responseNode(n)
		}
		me.mu.Unlock()

		if vs := m.Values(); vs != nil {
			nodeInfo := NodeInfo{
				Addr: t.remoteAddr,
			}
			copy(nodeInfo.ID[:], m.ID())
			select {
			case me.peerStream.values <- peerStreamValue{
				Peers:    vs,
				NodeInfo: nodeInfo,
			}:
			case <-me.peerStream.stop:
			}
		}

		if at, ok := m.AnnounceToken(); ok {
			me.announcePeer(addr, at)
		}

		me.mu.Lock()
		me.transactionClosed()
		me.mu.Unlock()
	})
	return nil
}

type peerStreamValue struct {
	Peers    []util.CompactPeer // Peers given in get_peers response.
	NodeInfo                    // The node that gave the response.
}

// TODO: This was to be the shared publicly accessible part returned by DHT
// functions that stream peers. Possibly not necessary anymore.
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
