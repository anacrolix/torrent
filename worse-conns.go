package torrent

import (
	"container/heap"
	"fmt"
	"time"
	"unsafe"

	"github.com/anacrolix/multiless"
)

type worseConnInput struct {
	Useful             bool
	LastHelpful        time.Time
	CompletedHandshake time.Time
	PeerPriority       peerPriority
	PeerPriorityErr    error
	Pointer            uintptr
}

func worseConnInputFromPeer(p *Peer) worseConnInput {
	ret := worseConnInput{
		Useful:             p.useful(),
		LastHelpful:        p.lastHelpful(),
		CompletedHandshake: p.completedHandshake,
		Pointer:            uintptr(unsafe.Pointer(p)),
	}
	ret.PeerPriority, ret.PeerPriorityErr = p.peerPriority()
	return ret
}

func worseConn(_l, _r *Peer) bool {
	return worseConnInputFromPeer(_l).Less(worseConnInputFromPeer(_r))
}

func (l worseConnInput) Less(r worseConnInput) bool {
	less, ok := multiless.New().Bool(
		l.Useful, r.Useful).CmpInt64(
		l.LastHelpful.Sub(r.LastHelpful).Nanoseconds()).CmpInt64(
		l.CompletedHandshake.Sub(r.CompletedHandshake).Nanoseconds()).LazySameLess(
		func() (same, less bool) {
			same = l.PeerPriorityErr != nil || r.PeerPriorityErr != nil || l.PeerPriority == r.PeerPriority
			less = l.PeerPriority < r.PeerPriority
			return
		}).Uintptr(
		l.Pointer, r.Pointer,
	).LessOk()
	if !ok {
		panic(fmt.Sprintf("cannot differentiate %#v and %#v", l, r))
	}
	return less
}

type worseConnSlice struct {
	conns []*PeerConn
}

var _ heap.Interface = &worseConnSlice{}

func (me worseConnSlice) Len() int {
	return len(me.conns)
}

func (me worseConnSlice) Less(i, j int) bool {
	return worseConn(&me.conns[i].Peer, &me.conns[j].Peer)
}

func (me *worseConnSlice) Pop() interface{} {
	i := len(me.conns) - 1
	ret := me.conns[i]
	me.conns = me.conns[:i]
	return ret
}

func (me *worseConnSlice) Push(x interface{}) {
	me.conns = append(me.conns, x.(*PeerConn))
}

func (me worseConnSlice) Swap(i, j int) {
	me.conns[i], me.conns[j] = me.conns[j], me.conns[i]
}
