package dht

// get_peers and announce_peers.

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/anacrolix/missinggo/v2/conntrack"
	"github.com/anacrolix/stm"
	"github.com/anacrolix/stm/stmutil"
	"github.com/benbjohnson/immutable"

	"github.com/anacrolix/dht/v2/krpc"
)

// Maintains state for an ongoing Announce operation. An Announce is started by calling
// Server.Announce.
type Announce struct {
	Peers chan PeersValues

	values chan PeersValues // Responses are pushed to this channel.

	// These only exist to support routines relying on channels for synchronization.
	done    <-chan struct{}
	doneVar *stm.Var
	cancel  func()

	pending  *stm.Var // How many transactions are still ongoing (int).
	server   *Server
	infoHash int160 // Target
	// Count of (probably) distinct addresses we've sent get_peers requests to.
	numContacted int64
	// The torrent port that we're announcing.
	announcePort int
	// The torrent port should be determined by the receiver in case we're
	// being NATed.
	announcePortImplied bool

	// List of pendingAnnouncePeer. TODO: Perhaps this should be sorted by distance to the target,
	// so we can do that sloppy hash stuff ;).
	pendingAnnouncePeers *stm.Var

	traversal traversal
}

func (a *Announce) String() string {
	return fmt.Sprintf("%[1]T %[1]p of %v on %v", a, a.infoHash, a.server)
}

type pendingAnnouncePeer struct {
	Addr
	token string
}

// Returns the number of distinct remote addresses the announce has queried.
func (a *Announce) NumContacted() int64 {
	return atomic.LoadInt64(&a.numContacted)
}

// Traverses the DHT graph toward nodes that store peers for the infohash, streaming them to the
// caller, and announcing the local node to each responding node if port is non-zero or impliedPort
// is true.
func (s *Server) Announce(infoHash [20]byte, port int, impliedPort bool) (*Announce, error) {
	startAddrs, err := s.traversalStartingNodes()
	if err != nil {
		return nil, err
	}
	infoHashInt160 := int160FromByteArray(infoHash)
	a := &Announce{
		Peers:                make(chan PeersValues, 100),
		values:               make(chan PeersValues),
		server:               s,
		infoHash:             infoHashInt160,
		announcePort:         port,
		announcePortImplied:  impliedPort,
		pending:              stm.NewVar(0),
		pendingAnnouncePeers: stm.NewVar(immutable.NewList()),
		traversal:            newTraversal(infoHashInt160),
	}
	var ctx context.Context
	ctx, a.cancel = context.WithCancel(context.Background())
	a.done = ctx.Done()
	a.doneVar, _ = stmutil.ContextDoneVar(ctx)
	// Function ferries from values to Peers until discovery is halted.
	go func() {
		defer close(a.Peers)
		for {
			select {
			case psv := <-a.values:
				select {
				case a.Peers <- psv:
				case <-a.done:
					return
				}
			case <-a.done:
				return
			}
		}
	}()
	for _, n := range startAddrs {
		stm.Atomically(a.pendContact(n))
	}
	go a.closer()
	go a.nodeContactor()
	return a, nil
}

func (a *Announce) closer() {
	defer a.cancel()
	stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
		if tx.Get(a.doneVar).(bool) {
			return
		}
		tx.Assert(tx.Get(a.pending).(int) == 0)
		a.traversal.waitFinished(tx)
		tx.Assert(tx.Get(a.pendingAnnouncePeers).(stmutil.Lenner).Len() == 0)
	}))
}

func validNodeAddr(addr net.Addr) bool {
	// At least for UDP addresses, we know what doesn't work.
	ua := addr.(*net.UDPAddr)
	if ua.Port == 0 {
		return false
	}
	if ip4 := ua.IP.To4(); ip4 != nil && ip4[0] == 0 {
		// Why?
		return false
	}
	return true
}

func (a *Announce) shouldContact(addr krpc.NodeAddr, tx *stm.Tx) bool {
	if !validNodeAddr(addr.UDP()) {
		return false
	}
	if a.server.ipBlocked(addr.IP) {
		return false
	}
	return true
}

func (a *Announce) responseNode(node krpc.NodeInfo) {
	i := int160FromByteArray(node.ID)
	stm.Atomically(a.pendContact(addrMaybeId{node.Addr, &i}))
}

// Announce to a peer, if appropriate.
func (a *Announce) maybeAnnouncePeer(to Addr, token *string, peerId *krpc.ID) {
	if token == nil {
		return
	}
	if !a.server.config.NoSecurity && (peerId == nil || !NodeIdSecure(*peerId, to.IP())) {
		return
	}
	stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
		tx.Set(a.pendingAnnouncePeers, tx.Get(a.pendingAnnouncePeers).(stmutil.List).Append(pendingAnnouncePeer{
			Addr:  to,
			token: *token,
		}))
	}))
	//a.server.announcePeer(to, a.infoHash, a.announcePort, *token, a.announcePortImplied, nil)
}

func (a *Announce) announcePeer(peer pendingAnnouncePeer) numWrites {
	_, writes, _ := a.server.announcePeer(peer.Addr, a.infoHash, a.announcePort, peer.token, a.announcePortImplied)
	return writes
}

func (a *Announce) beginAnnouncePeer(tx *stm.Tx) interface{} {
	l := tx.Get(a.pendingAnnouncePeers).(stmutil.List)
	tx.Assert(l.Len() != 0)
	x := l.Get(0).(pendingAnnouncePeer)
	tx.Set(a.pendingAnnouncePeers, l.Slice(1, l.Len()))
	return a.beginQuery(x.Addr, "dht announce announce_peer", func() numWrites {
		return a.announcePeer(x)
	})(tx).(func())
}

func finalizeCteh(cteh *conntrack.EntryHandle, writes numWrites) {
	if writes == 0 {
		cteh.Forget()
		// TODO: panic("how to reverse rate limit?")
	} else {
		cteh.Done()
	}
}

func (a *Announce) getPeers(addr Addr) numWrites {
	// log.Printf("sending get_peers to %v", node)
	m, writes, err := a.server.getPeers(context.TODO(), addr, a.infoHash)
	a.server.logger().Printf("Announce.server.getPeers result: m.Y=%v, numWrites=%v, err=%v", m.Y, writes, err)
	// log.Printf("get_peers response error from %v: %v", node, err)
	// Register suggested nodes closer to the target info-hash.
	if m.R != nil && m.SenderID() != nil {
		expvars.Add("announce get_peers response nodes values", int64(len(m.R.Nodes)))
		expvars.Add("announce get_peers response nodes6 values", int64(len(m.R.Nodes6)))
		m.R.ForAllNodes(a.responseNode)
		select {
		case a.values <- PeersValues{
			Peers: m.R.Values,
			NodeInfo: krpc.NodeInfo{
				Addr: addr.KRPC(),
				ID:   *m.SenderID(),
			},
		}:
		case <-a.done:
		}
		a.maybeAnnouncePeer(addr, m.R.Token, m.SenderID())
	}
	return writes
}

// Corresponds to the "values" key in a get_peers KRPC response. A list of
// peers that a node has reported as being in the swarm for a queried info
// hash.
type PeersValues struct {
	Peers         []Peer // Peers given in get_peers response.
	krpc.NodeInfo        // The node that gave the response.
}

// Stop the announce.
func (a *Announce) Close() {
	a.close()
}

func (a *Announce) close() {
	a.cancel()
}

func (a *Announce) pendContact(node addrMaybeId) stm.Operation {
	return stm.VoidOperation(func(tx *stm.Tx) {
		if !a.shouldContact(node.Addr, tx) {
			// log.Printf("shouldn't contact (pend): %v", node)
			return
		}
		a.traversal.pendContact(node)(tx)
	})
}

type txResT struct {
	done bool
	run  func()
}

func (a *Announce) nodeContactor() {
	for {
		txRes := stm.Atomically(func(tx *stm.Tx) interface{} {
			if tx.Get(a.doneVar).(bool) {
				return txResT{done: true}
			}
			return txResT{run: stm.Select(
				a.beginGetPeers,
				a.beginAnnouncePeer,
			)(tx).(func())}
		}).(txResT)
		if txRes.done {
			break
		}
		go txRes.run()
	}
}

func (a *Announce) beginGetPeers(tx *stm.Tx) interface{} {
	addr := a.traversal.nextAddr(tx)
	dhtAddr := NewAddr(addr.UDP())
	return a.beginQuery(dhtAddr, "dht announce get_peers", func() numWrites {
		atomic.AddInt64(&a.numContacted, 1)
		return a.getPeers(dhtAddr)
	})(tx)
}

func (a *Announce) beginQuery(addr Addr, reason string, f func() numWrites) stm.Operation {
	return func(tx *stm.Tx) interface{} {
		tx.Set(a.pending, tx.Get(a.pending).(int)+1)
		return a.server.beginQuery(addr, reason, func() numWrites {
			a.server.logger().Printf("doing %s to %v", reason, addr)
			defer stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) { tx.Set(a.pending, tx.Get(a.pending).(int)-1) }))
			return f()
		})(tx)
	}
}
