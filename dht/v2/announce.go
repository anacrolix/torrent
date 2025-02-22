package dht

// get_peers and announce_peers.

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"

	"github.com/anacrolix/stm"
	"github.com/benbjohnson/immutable"
	"github.com/james-lawrence/torrent/internal/stmutil"

	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

// Maintains state for an ongoing Announce operation. An Announce is started by calling
// Server.Announce.
type Announce struct {
	Peers chan PeersValues

	// These only exist to support routines relying on channels for synchronization.
	done    <-chan struct{}
	doneVar *stm.Var[bool]
	cancel  func()

	pending  *stm.Var[int] // How many transactions are still ongoing (int).
	server   *Server
	infoHash Int160 // Target
	// Count of (probably) distinct addresses we've sent get_peers requests to.
	numContacted int64
	// The torrent port that we're announcing.
	announcePort int
	// The torrent port should be determined by the receiver in case we're
	// being NATed.
	announcePortImplied bool

	// List of pendingAnnouncePeer. TODO: Perhaps this should be sorted by distance to the target,
	// so we can do that sloppy hash stuff ;).
	pendingAnnouncePeers *stm.Var[*immutable.List[pendingAnnouncePeer]]

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
func (s *Server) Announce(ctx context.Context, infoHash [20]byte, port int, impliedPort bool) (*Announce, error) {
	startAddrs, err := s.traversalStartingNodes()
	if err != nil {
		return nil, err
	}

	infoHashInt160 := Int160FromByteArray(infoHash)
	a := &Announce{
		Peers:                make(chan PeersValues, 100),
		server:               s,
		infoHash:             infoHashInt160,
		announcePort:         port,
		announcePortImplied:  impliedPort,
		pending:              stm.NewVar(0),
		pendingAnnouncePeers: stm.NewVar(immutable.NewList[pendingAnnouncePeer]()),
		traversal:            newTraversal(infoHashInt160),
	}
	ctx, a.cancel, a.doneVar = stmutil.ContextDoneVar(ctx)
	a.done = ctx.Done()

	log.Println("ANNOUNCING WITH", len(startAddrs))
	for _, n := range startAddrs {
		stm.Atomically(a.pendContact(n))
	}
	go a.closer()
	go a.nodeContactor()
	return a, nil
}

func (a *Announce) closer() {
	defer log.Println("announce closed", a.infoHash)
	defer a.cancel()
	stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
		if a.doneVar.Get(tx) {
			return
		}
		tx.Assert(a.pending.Get(tx) == 0)
		a.traversal.waitFinished(tx)
		tx.Assert(a.pendingAnnouncePeers.Get(tx).Len() == 0)
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

func (a *Announce) shouldContact(addr krpc.NodeAddr, _ *stm.Tx) bool {
	if !validNodeAddr(addr.UDP()) {
		return false
	}
	if a.server.ipBlocked(addr.IP.AsSlice()) {
		return false
	}
	return true
}

func (a *Announce) responseNode(node krpc.NodeInfo) {
	i := Int160FromByteArray(node.ID)
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
		a.pendingAnnouncePeers.Set(tx, a.pendingAnnouncePeers.Get(tx).Append(pendingAnnouncePeer{
			Addr:  to,
			token: *token,
		}))
	}))
	a.server.announcePeer(to, a.infoHash, a.announcePort, *token, a.announcePortImplied)
}

func (a *Announce) announcePeer(peer pendingAnnouncePeer) numWrites {
	_, writes, _ := a.server.announcePeer(peer.Addr, a.infoHash, a.announcePort, peer.token, a.announcePortImplied)
	return writes
}

func (a *Announce) getPeers(ctx context.Context, addr Addr) numWrites {
	// log.Printf("sending get_peers to %v", addr.String())
	m, writes, err := a.server.getPeers(ctx, addr, a.infoHash)
	if err != nil {
		log.Printf("get_peers response error from %v: %v", addr, err)
		return writes
	}

	// a.server.logger().Printf("Announce.server.getPeers result: m.Y=%v, numWrites=%v, err=%v", m.Y, writes, err)
	log.Printf("get_peers response error from %v: %v", addr.String(), err)
	// Register suggested nodes closer to the target info-hash.
	if m.R != nil && m.SenderID() != nil {
		m.R.ForAllNodes(a.responseNode)
		select {
		case a.Peers <- PeersValues{
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

func (a *Announce) pendContact(node addrMaybeId) stm.Operation[struct{}] {
	return stm.VoidOperation(func(tx *stm.Tx) {
		if !a.shouldContact(node.Addr, tx) {
			log.Printf("shouldn't contact (pend): %v", node)
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
	log.Println("node contacting initiated", a.infoHash)
	defer log.Println("node contacting completed", a.infoHash)
	for {
		txRes := stm.Atomically(func(tx *stm.Tx) txResT {
			if a.doneVar.Get(tx) {
				return txResT{done: true}
			}

			res := stm.Select(
				a.beginGetPeers,
				a.beginAnnouncePeer,
			)(tx)

			return txResT{run: res}
		})
		if txRes.done {
			break
		}
		go txRes.run()
	}
}

func (a *Announce) beginAnnouncePeer(tx *stm.Tx) func() {
	l := a.pendingAnnouncePeers.Get(tx)
	tx.Assert(l.Len() != 0)
	x := l.Get(0)
	a.pendingAnnouncePeers.Set(tx, l.Slice(1, l.Len()))
	res := a.beginQuery(x.Addr, "dht announce announce_peer", func() numWrites {
		return a.announcePeer(x)
	})(tx)
	log.Println("announce peer res", res != nil)
	tx.Assert(res != nil)
	return res
}

func (a *Announce) beginGetPeers(tx *stm.Tx) func() {
	// defer func() {
	// 	if err := recover(); err != nil {
	// 		log.Printf("PANIC? %T - %v\n", err, err)
	// 	}
	// }()
	addr := a.traversal.nextAddr(tx)
	dhtAddr := NewAddr(addr.UDP())

	res := a.beginQuery(dhtAddr, "dht announce get_peers", func() numWrites {
		defer atomic.AddInt64(&a.numContacted, 1)
		return a.getPeers(context.TODO(), dhtAddr)
	})(tx)
	return res
}

func (a *Announce) beginQuery(addr Addr, reason string, f func() numWrites) stm.Operation[func()] {
	return func(tx *stm.Tx) func() {
		a.pending.Set(tx, a.pending.Get(tx)+1)
		res := a.server.beginQuery(addr, reason, func() numWrites {
			a.server.logger().Printf("doing %s to %v", reason, addr)
			defer stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
				a.pending.Set(tx, a.pending.Get(tx)-1)
			}))
			return f()
		})(tx)
		tx.Assert(res != nil)
		return res
	}
}
