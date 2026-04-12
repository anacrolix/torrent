package torrent

import (
	"hash/maphash"
	"net/netip"
	"strconv"
	"strings"

	"github.com/google/btree"
)

// Peers are stored with their priority at insertion. Their priority may
// change if our apparent IP changes, we don't currently handle that.
type prioritizedPeersItem struct {
	prio     peerPriority
	addrHash int64
	p        PeerInfo
}

func (me prioritizedPeersItem) Less(than btree.Item) bool {
	other := than.(prioritizedPeersItem)
	if me.p.Trusted != other.p.Trusted {
		return !me.p.Trusted && other.p.Trusted
	}
	if me.prio != other.prio {
		return me.prio < other.prio
	}
	return me.addrHash < other.addrHash
}

var hashSeed = maphash.MakeSeed()

// newPrioritizedPeersItem materializes the tree key once so repeated B-tree comparisons can reuse
// the priority fields and stable address hash without recomputing them.
func newPrioritizedPeersItem(prio peerPriority, p PeerInfo) prioritizedPeersItem {
	var h maphash.Hash
	h.SetSeed(hashSeed)
	writePeerRemoteAddrHash(&h, p.Addr)
	return prioritizedPeersItem{
		prio:     prio,
		addrHash: int64(h.Sum64()),
		p:        p,
	}
}

// writePeerRemoteAddrHash writes the same byte representation as addr.String() into the hash, using
// concrete address types to avoid building an intermediate string when possible.
func writePeerRemoteAddrHash(h *maphash.Hash, addr PeerRemoteAddr) {
	switch v := addr.(type) {
	case StringAddr:
		h.WriteString(string(v))
	case ipPortAddr:
		writeIPPortAddrStringHash(h, v)
	case netip.AddrPort:
		var buf [64]byte
		h.Write(v.AppendTo(buf[:0]))
	default:
		h.WriteString(addr.String())
	}
}

// writeIPPortAddrStringHash encodes ipPortAddr in net.JoinHostPort-compatible form so the cached
// hash preserves the original tree ordering for IPv4 and IPv6 addresses.
func writeIPPortAddrStringHash(h *maphash.Hash, addr ipPortAddr) {
	host := addr.IP.String()
	if strings.IndexByte(host, ':') >= 0 {
		h.WriteString("[")
		h.WriteString(host)
		h.WriteString("]")
	} else {
		h.WriteString(host)
	}
	h.WriteString(":")
	var portBuf [20]byte
	h.Write(strconv.AppendInt(portBuf[:0], int64(addr.Port), 10))
}

type prioritizedPeers struct {
	om      *btree.BTree
	getPrio func(PeerInfo) peerPriority
}

func (me *prioritizedPeers) Each(f func(PeerInfo)) {
	me.om.Ascend(func(i btree.Item) bool {
		f(i.(prioritizedPeersItem).p)
		return true
	})
}

func (me *prioritizedPeers) Len() int {
	if me == nil || me.om == nil {
		return 0
	}
	return me.om.Len()
}

// Returns true if a peer is replaced.
func (me *prioritizedPeers) Add(p PeerInfo) bool {
	return me.om.ReplaceOrInsert(newPrioritizedPeersItem(me.getPrio(p), p)) != nil
}

// Returns true if a peer is replaced.
func (me *prioritizedPeers) AddReturningReplacedPeer(p PeerInfo) (ret PeerInfo, ok bool) {
	item := me.om.ReplaceOrInsert(newPrioritizedPeersItem(me.getPrio(p), p))
	if item == nil {
		return
	}
	ret = item.(prioritizedPeersItem).p
	ok = true
	return
}

func (me *prioritizedPeers) DeleteMin() (ret prioritizedPeersItem, ok bool) {
	i := me.om.DeleteMin()
	if i == nil {
		return
	}
	ret = i.(prioritizedPeersItem)
	ok = true
	return
}

func (me *prioritizedPeers) PopMax() PeerInfo {
	return me.om.DeleteMax().(prioritizedPeersItem).p
}
