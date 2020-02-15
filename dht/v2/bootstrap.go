package dht

import (
	"sync/atomic"

	"github.com/anacrolix/stm"

	"github.com/anacrolix/dht/v2/krpc"
)

// Populates the node table.
func (s *Server) Bootstrap() (ts TraversalStats, err error) {
	initialAddrs, err := s.traversalStartingNodes()
	if err != nil {
		return
	}
	traversal := newTraversal(s.id)
	for _, addr := range initialAddrs {
		stm.Atomically(traversal.pendContact(addr))
	}
	outstanding := stm.NewVar(0)
	for {
		type txResT struct {
			done bool
			io   func()
		}
		txRes := stm.Atomically(stm.Select(
			func(tx *stm.Tx) interface{} {
				addr := traversal.nextAddr(tx)
				dhtAddr := NewAddr(addr.UDP())
				tx.Set(outstanding, tx.Get(outstanding).(int)+1)
				return txResT{
					io: s.beginQuery(dhtAddr, "dht bootstrap find_node", func() numWrites {
						atomic.AddInt64(&ts.NumAddrsTried, 1)
						m, writes, err := s.findNode(dhtAddr, s.id)
						if err == nil {
							atomic.AddInt64(&ts.NumResponses, 1)
						}
						if r := m.R; r != nil {
							r.ForAllNodes(func(ni krpc.NodeInfo) {
								id := int160FromByteArray(ni.ID)
								stm.Atomically(traversal.pendContact(addrMaybeId{
									Addr: ni.Addr,
									Id:   &id,
								}))
							})
						}
						stm.Atomically(stm.VoidOperation(func(tx *stm.Tx) {
							tx.Set(outstanding, tx.Get(outstanding).(int)-1)
						}))
						return writes
					})(tx).(func()),
				}
			},
			func(tx *stm.Tx) interface{} {
				tx.Assert(tx.Get(outstanding).(int) == 0)
				return txResT{done: true}
			},
		)).(txResT)
		if txRes.done {
			break
		}
		go txRes.io()
	}
	return
}
