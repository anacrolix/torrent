package traversal

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/chansync/events"
	"github.com/anacrolix/sync"

	"github.com/anacrolix/dht/v2/containers"
	"github.com/anacrolix/dht/v2/int160"
	k_nearest_nodes "github.com/anacrolix/dht/v2/k-nearest-nodes"
	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/dht/v2/types"
)

type QueryResult struct {
	// This is set non-nil if a query reply is a response-type as defined by the DHT BEP 5 (contains
	// "r")
	ResponseFrom *krpc.NodeInfo
	// Data associated with a closest node. Is this ever not a string? I think using generics for
	// this leaks throughout the entire Operation. Hardly worth it. It's still possible to handle
	// invalid token types at runtime.
	ClosestData interface{}
	Nodes       []krpc.NodeInfo
	Nodes6      []krpc.NodeInfo
}

type OperationInput struct {
	Target     krpc.ID
	Alpha      int
	K          int
	DoQuery    func(context.Context, krpc.NodeAddr) QueryResult
	NodeFilter func(types.AddrMaybeId) bool
	// This filters the adding of nodes to the "closest data" set based on the data they provided.
	// The data is (usually?) derived from the token field in a reply. For the get_peers traversal
	// operation for example, we would filter out non-strings, since we later need to pass strings
	// in to the Token field to announce ourselves to the closest nodes we found to the target.
	DataFilter func(data any) bool
}

type defaultsAppliedOperationInput OperationInput

func Start(input OperationInput) *Operation {
	herp := defaultsAppliedOperationInput(input)
	if herp.Alpha == 0 {
		herp.Alpha = 3
	}
	if herp.K == 0 {
		herp.K = 8
	}
	if herp.NodeFilter == nil {
		herp.NodeFilter = func(types.AddrMaybeId) bool {
			return true
		}
	}
	if herp.DataFilter == nil {
		herp.DataFilter = func(_ any) bool {
			return true
		}
	}
	targetInt160 := herp.Target.Int160()
	op := &Operation{
		targetInt160: targetInt160,
		input:        herp,
		queried:      make(map[addrString]struct{}),
		closest:      k_nearest_nodes.New(targetInt160, herp.K),
		unqueried:    containers.NewImmutableAddrMaybeIdsByDistance(targetInt160),
	}
	go op.run()
	return op
}

type addrString string

type Operation struct {
	stats        Stats
	mu           sync.Mutex
	unqueried    containers.AddrMaybeIdsByDistance
	queried      map[addrString]struct{}
	closest      k_nearest_nodes.Type
	targetInt160 int160.T
	input        defaultsAppliedOperationInput
	outstanding  int
	cond         chansync.BroadcastCond
	stalled      chansync.LevelTrigger
	stopping     chansync.SetOnce
	stopped      chansync.SetOnce
}

// I don't think you should access this until the Stopped event.
func (op *Operation) Stats() *Stats {
	return &op.stats
}

func (op *Operation) Stop() {
	if op.stopping.Set() {
		go func() {
			defer op.stopped.Set()
			op.mu.Lock()
			defer op.mu.Unlock()
			for {
				if op.outstanding == 0 {
					break
				}
				cond := op.cond.Signaled()
				op.mu.Unlock()
				<-cond
				op.mu.Lock()
			}
		}()
	}
}

func (op *Operation) Stopped() events.Done {
	return op.stopped.Done()
}

func (op *Operation) Stalled() events.Active {
	return op.stalled.Active()
}

func (op *Operation) addNodeLocked(n types.AddrMaybeId) (err error) {
	if _, ok := op.queried[addrString(n.Addr.String())]; ok {
		err = errors.New("already queried")
		return
	}
	if !op.input.NodeFilter(n) {
		err = errors.New("failed filter")
		return
	}
	op.unqueried = op.unqueried.Add(n)
	op.cond.Broadcast()
	return nil
}

// Add an unqueried node returning an error with a reason why the node wasn't added.
func (op *Operation) AddNode(n types.AddrMaybeId) (err error) {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.addNodeLocked(n)
}

// Add a bunch of unqueried nodes at once, returning how many were successfully added.
func (op *Operation) AddNodes(nodes []types.AddrMaybeId) (added int) {
	op.mu.Lock()
	defer op.mu.Unlock()
	before := op.unqueried.Len()
	for _, n := range nodes {
		_ = op.addNodeLocked(n)
	}
	return op.unqueried.Len() - before
}

func (op *Operation) markQueried(addr krpc.NodeAddrPort) {
	op.queried[addrString(addr.String())] = struct{}{}
}

func (op *Operation) closestUnqueried() (ret types.AddrMaybeId) {
	return op.unqueried.Next()
}

func (op *Operation) popClosestUnqueried() types.AddrMaybeId {
	ret := op.closestUnqueried()
	op.unqueried = op.unqueried.Delete(ret)
	return ret
}

func (op *Operation) haveQuery() bool {
	if op.unqueried.Len() == 0 {
		return false
	}
	if !op.closest.Full() {
		return true
	}
	cu := op.closestUnqueried()
	if !cu.Id.Ok {
		return false
	}
	cuDist := cu.Id.Value.Distance(op.targetInt160)
	farDist := op.closest.Farthest().ID.Int160().Distance(op.targetInt160)
	return cuDist.Cmp(farDist) <= 0
}

func (op *Operation) run() {
	defer close(op.stalled.Signal())
	op.mu.Lock()
	defer op.mu.Unlock()
	for {
		if op.stopping.IsSet() {
			return
		}
		for op.outstanding < op.input.Alpha && op.haveQuery() {
			op.startQuery()
		}
		var stalled events.Signal
		if (!op.haveQuery() || op.input.Alpha == 0) && op.outstanding == 0 {
			stalled = op.stalled.Signal()
		}
		queryCondSignaled := op.cond.Signaled()
		op.mu.Unlock()
		select {
		case stalled <- struct{}{}:
		case <-op.stopping.Done():
		case <-queryCondSignaled:
		}
		op.mu.Lock()
	}
}

func (op *Operation) addClosest(node krpc.NodeInfo, data interface{}) {
	var ami types.AddrMaybeId
	ami.FromNodeInfo(node)
	if !op.input.NodeFilter(ami) {
		return
	}
	if !op.input.DataFilter(data) {
		return
	}
	op.closest = op.closest.Push(k_nearest_nodes.Elem{
		Key:  node.ToNodeInfoAddrPort(),
		Data: data,
	})
}

func (op *Operation) Closest() *k_nearest_nodes.Type {
	return &op.closest
}

func (op *Operation) startQuery() {
	a := op.popClosestUnqueried()
	op.markQueried(a.Addr)
	op.outstanding++
	go func() {
		defer func() {
			op.mu.Lock()
			defer op.mu.Unlock()
			op.outstanding--
			op.cond.Broadcast()
		}()
		// log.Printf("traversal querying %v", a)
		atomic.AddUint32(&op.stats.NumAddrsTried, 1)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-ctx.Done():
			case <-op.stopping.Done():
				cancel()
			}
		}()
		res := op.input.DoQuery(ctx, a.Addr.ToNodeAddr())
		cancel()
		if res.ResponseFrom != nil {
			func() {
				op.mu.Lock()
				defer op.mu.Unlock()
				atomic.AddUint32(&op.stats.NumResponses, 1)
				op.addClosest(*res.ResponseFrom, res.ClosestData)
			}()
		}
		op.AddNodes(types.AddrMaybeIdSliceFromNodeInfoSlice(res.Nodes))
		op.AddNodes(types.AddrMaybeIdSliceFromNodeInfoSlice(res.Nodes6))
	}()
}
