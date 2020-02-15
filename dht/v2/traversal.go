package dht

import (
	"fmt"

	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/anacrolix/stm"
	"github.com/anacrolix/stm/stmutil"

	"github.com/anacrolix/dht/v2/krpc"
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
	targetInfohash      int160
	triedAddrs          *stm.Var // Settish of krpc.NodeAddr.String
	nodesPendingContact *stm.Var // Settish of addrMaybeId sorted by distance from the target
	addrBestIds         *stm.Var // Mappish Addr to best
}

func newTraversal(targetInfohash int160) traversal {
	return traversal{
		targetInfohash:      targetInfohash,
		triedAddrs:          stm.NewVar(stmutil.NewSet()),
		nodesPendingContact: stm.NewVar(nodesByDistance(targetInfohash)),
		addrBestIds:         stm.NewVar(stmutil.NewMap()),
	}
}

func (t traversal) waitFinished(tx *stm.Tx) {
	tx.Assert(tx.Get(t.nodesPendingContact).(stmutil.Lenner).Len() == 0)
}

func (t traversal) pendContact(node addrMaybeId) stm.Operation {
	return stm.VoidOperation(func(tx *stm.Tx) {
		nodeAddrString := node.Addr.String()
		if tx.Get(t.triedAddrs).(stmutil.Settish).Contains(nodeAddrString) {
			return
		}
		addrBestIds := tx.Get(t.addrBestIds).(stmutil.Mappish)
		nodesPendingContact := tx.Get(t.nodesPendingContact).(stmutil.Settish)
		if _best, ok := addrBestIds.Get(nodeAddrString); ok {
			if node.Id == nil {
				return
			}
			best := _best.(*int160)
			if best != nil && distance(*best, t.targetInfohash).Cmp(distance(*node.Id, t.targetInfohash)) <= 0 {
				return
			}
			nodesPendingContact = nodesPendingContact.Delete(addrMaybeId{
				Addr: node.Addr,
				Id:   best,
			})
		}
		tx.Set(t.addrBestIds, addrBestIds.Set(nodeAddrString, node.Id))
		nodesPendingContact = nodesPendingContact.Add(node)
		tx.Set(t.nodesPendingContact, nodesPendingContact)
	})
}

func (a traversal) nextAddr(tx *stm.Tx) krpc.NodeAddr {
	npc := tx.Get(a.nodesPendingContact).(stmutil.Settish)
	first, ok := iter.First(npc.Iter)
	tx.Assert(ok)
	addr := first.(addrMaybeId).Addr
	addrString := addr.String()
	tx.Set(a.nodesPendingContact, npc.Delete(first))
	tx.Set(a.addrBestIds, tx.Get(a.addrBestIds).(stmutil.Mappish).Delete(addrString))
	tx.Set(a.triedAddrs, tx.Get(a.triedAddrs).(stmutil.Settish).Add(addrString))
	return addr
}
