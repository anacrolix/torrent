package torrent

import (
	"container/heap"
	"fmt"
	"time"
	"unsafe"
)

type worseConnInput struct {
	BadDirection       bool
	Useful             bool
	LastHelpful        time.Time
	CompletedHandshake time.Time
	GetPeerPriority    func() (peerPriority, error)
	Pointer            uintptr

	peerPriority     peerPriority
	peerPriorityErr  error
	peerPriorityDone bool
}

// getPeerPriority memoizes the peer priority lookup. Ranking runs single-threaded under the client
// lock, so this doesn't need to synchronize.
func (me *worseConnInput) getPeerPriority() (peerPriority, error) {
	if !me.peerPriorityDone {
		me.peerPriority, me.peerPriorityErr = me.GetPeerPriority()
		me.peerPriorityDone = true
	}
	return me.peerPriority, me.peerPriorityErr
}

type worseConnLensOpts struct {
	incomingIsBad, outgoingIsBad bool
}

// worseConnInputFromPeer snapshots the peer fields used by connection ranking so heap operations
// compare stable values instead of repeatedly reading live peer state.
func worseConnInputFromPeer(dst *worseConnInput, p *PeerConn, opts worseConnLensOpts) {
	dst.BadDirection = false
	dst.Useful = p.useful()
	dst.LastHelpful = p.lastHelpful()
	dst.CompletedHandshake = p.completedHandshake
	dst.GetPeerPriority = p.peerPriority
	dst.peerPriority = 0
	dst.peerPriorityErr = nil
	dst.peerPriorityDone = false
	dst.Pointer = uintptr(unsafe.Pointer(p))
	if opts.incomingIsBad && !p.outgoing {
		dst.BadDirection = true
	} else if opts.outgoingIsBad && p.outgoing {
		dst.BadDirection = true
	}
}

// Less applies the connection ordering from lowest to highest desirability, deferring peer-priority
// lookup until earlier fields tie and falling back to the pointer for a total ordering.
func (l *worseConnInput) Less(r *worseConnInput) bool {
	if l.BadDirection != r.BadDirection {
		return l.BadDirection && !r.BadDirection
	}
	if l.Useful != r.Useful {
		return !l.Useful && r.Useful
	}
	if !l.LastHelpful.Equal(r.LastHelpful) {
		return l.LastHelpful.Before(r.LastHelpful)
	}
	if !l.CompletedHandshake.Equal(r.CompletedHandshake) {
		return l.CompletedHandshake.Before(r.CompletedHandshake)
	}
	lPeerPriority, lPeerPriorityErr := l.getPeerPriority()
	if lPeerPriorityErr == nil {
		rPeerPriority, rPeerPriorityErr := r.getPeerPriority()
		if rPeerPriorityErr == nil && lPeerPriority != rPeerPriority {
			return lPeerPriority < rPeerPriority
		}
	}
	if l.Pointer == r.Pointer {
		panic(fmt.Sprintf("cannot differentiate %#v and %#v", l, r))
	}
	return l.Pointer < r.Pointer
}

type worseConnSlice struct {
	conns      []*PeerConn
	keys       []*worseConnInput
	keyStorage []worseConnInput
}

// initKeys builds contiguous ranking keys for the heap so comparisons can reuse precomputed peer
// metadata without one allocation per entry.
func (me *worseConnSlice) initKeys(opts worseConnLensOpts) {
	// Keep the key structs contiguous so heap comparisons don't allocate one object per peer.
	me.keyStorage = make([]worseConnInput, len(me.conns))
	me.keys = make([]*worseConnInput, len(me.conns))
	for i, c := range me.conns {
		worseConnInputFromPeer(&me.keyStorage[i], c, opts)
		me.keys[i] = &me.keyStorage[i]
	}
}

var _ heap.Interface = (*worseConnSlice)(nil)

func (me *worseConnSlice) Len() int {
	return len(me.conns)
}

func (me *worseConnSlice) Less(i, j int) bool {
	return me.keys[i].Less(me.keys[j])
}

func (me *worseConnSlice) Pop() interface{} {
	i := len(me.conns) - 1
	ret := me.conns[i]
	me.keys[i] = nil
	me.conns = me.conns[:i]
	me.keys = me.keys[:i]
	// keyStorage is only the arena the key pointers refer into. Swap permutes keys, not keyStorage,
	// so keyStorage[i] doesn't correspond to keys[i] and trimming it would drop an arbitrary entry.
	return ret
}

func (me *worseConnSlice) Push(x interface{}) {
	panic("not implemented")
}

func (me *worseConnSlice) Swap(i, j int) {
	me.conns[i], me.conns[j] = me.conns[j], me.conns[i]
	me.keys[i], me.keys[j] = me.keys[j], me.keys[i]
}
