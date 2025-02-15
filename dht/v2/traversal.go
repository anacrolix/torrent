package dht

import (
	"fmt"

	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/anacrolix/stm"
	"github.com/james-lawrence/torrent/internal/stmutil"

	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

type TraversalStats struct {
	NumAddrsTried int64
	NumResponses  int64
}

func (me TraversalStats) String() string {
	return fmt.Sprintf("%#v", me)
}

// Prioritizes addrs to try by distance from target, disallowing repeat contacts.
type traversal struct {
	targetInfohash      Int160
	triedAddrs          *stm.Var[stmutil.Settish[string]]          // Settish of krpc.NodeAddr.String
	nodesPendingContact *stm.Var[stmutil.Settish[addrMaybeId]]     // Settish of addrMaybeId sorted by distance from the target
	addrBestIds         *stm.Var[stmutil.Mappish[string, *Int160]] // Mappish Addr to best
}

func newTraversal(targetInfohash Int160) traversal {
	return traversal{
		targetInfohash:      targetInfohash,
		triedAddrs:          stm.NewVar(stmutil.NewSet[string]()),
		nodesPendingContact: stm.NewVar(nodesByDistance(targetInfohash)),
		addrBestIds:         stm.NewVar(stmutil.NewMap[string, *Int160]()),
	}
}

func (t traversal) waitFinished(tx *stm.Tx) {
	tx.Assert(t.nodesPendingContact.Get(tx).Len() == 0)
}

func (t traversal) pendContact(node addrMaybeId) stm.Operation[struct{}] {
	return stm.VoidOperation(func(tx *stm.Tx) {
		nodeAddrString := node.Addr.String()
		if t.triedAddrs.Get(tx).Contains(nodeAddrString) {
			return
		}
		addrBestIds := t.addrBestIds.Get(tx)
		nodesPendingContact := t.nodesPendingContact.Get(tx)
		if best, ok := addrBestIds.Get(nodeAddrString); ok {
			if node.Id == nil {
				return
			}

			if best != nil && distance(*best, t.targetInfohash).Cmp(distance(*node.Id, t.targetInfohash)) <= 0 {
				return
			}
			nodesPendingContact = nodesPendingContact.Delete(addrMaybeId{
				Addr: node.Addr,
				Id:   best,
			})
		}
		t.addrBestIds.Set(tx, addrBestIds.Set(nodeAddrString, node.Id))
		t.nodesPendingContact.Set(tx, nodesPendingContact.Add(node))
	})
}

func (a traversal) nextAddr(tx *stm.Tx) krpc.NodeAddr {
	npc := a.nodesPendingContact.Get(tx)
	first, ok := iter.First(npc.Iter)
	tx.Assert(ok)
	addr := first.(addrMaybeId).Addr
	addrString := addr.String()
	a.nodesPendingContact.Set(tx, npc.Delete(first.(addrMaybeId)))
	a.addrBestIds.Set(tx, a.addrBestIds.Get(tx).Delete(addrString))
	a.triedAddrs.Set(tx, a.triedAddrs.Get(tx).Add(addrString))
	return addr
}
