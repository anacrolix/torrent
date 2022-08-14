package torrent

import (
	"container/heap"
	"fmt"
	"time"
	"unsafe"

	"github.com/anacrolix/multiless"
	"github.com/anacrolix/sync"
)

type worseConnInput struct {
	Useful              bool
	LastHelpful         time.Time
	CompletedHandshake  time.Time
	GetPeerPriority     func() (peerPriority, error)
	getPeerPriorityOnce sync.Once
	peerPriority        peerPriority
	peerPriorityErr     error
	Pointer             uintptr
}

func (i *worseConnInput) doGetPeerPriority() {
	i.peerPriority, i.peerPriorityErr = i.GetPeerPriority()
}

func (i *worseConnInput) doGetPeerPriorityOnce() {
	i.getPeerPriorityOnce.Do(i.doGetPeerPriority)
}

func worseConnInputFromPeer(p *Peer) worseConnInput {
	ret := worseConnInput{
		Useful:             p.useful(),
		LastHelpful:        p.lastHelpful(),
		CompletedHandshake: p.completedHandshake,
		Pointer:            uintptr(unsafe.Pointer(p)),
		GetPeerPriority:    p.peerPriority,
	}
	return ret
}

func worseConn(_l, _r *Peer) bool {
	// TODO: Use generics for ptr to
	l := worseConnInputFromPeer(_l)
	r := worseConnInputFromPeer(_r)
	return l.Less(&r)
}

func (i *worseConnInput) Less(r *worseConnInput) bool {
	less, ok := multiless.New().Bool(
		i.Useful, r.Useful).CmpInt64(
		i.LastHelpful.Sub(r.LastHelpful).Nanoseconds()).CmpInt64(
		i.CompletedHandshake.Sub(r.CompletedHandshake).Nanoseconds()).LazySameLess(
		func() (same, less bool) {
			i.doGetPeerPriorityOnce()
			if i.peerPriorityErr != nil {
				same = true
				return
			}
			r.doGetPeerPriorityOnce()
			if r.peerPriorityErr != nil {
				same = true
				return
			}
			same = i.peerPriority == r.peerPriority
			less = i.peerPriority < r.peerPriority
			return
		}).Uintptr(
		i.Pointer, r.Pointer,
	).LessOk()
	if !ok {
		panic(fmt.Sprintf("cannot differentiate %#v and %#v", i, r))
	}
	return less
}

type worseConnSlice struct {
	conns []*PeerConn
	keys  []worseConnInput
}

func (s *worseConnSlice) initKeys() {
	s.keys = make([]worseConnInput, len(s.conns))
	for i, c := range s.conns {
		s.keys[i] = worseConnInputFromPeer(&c.Peer)
	}
}

var _ heap.Interface = &worseConnSlice{}

func (s worseConnSlice) Len() int {
	return len(s.conns)
}

func (s worseConnSlice) Less(i, j int) bool {
	return s.keys[i].Less(&s.keys[j])
}

func (s *worseConnSlice) Pop() interface{} {
	i := len(s.conns) - 1
	ret := s.conns[i]
	s.conns = s.conns[:i]
	return ret
}

func (s *worseConnSlice) Push(x interface{}) {
	panic("not implemented")
}

func (s worseConnSlice) Swap(i, j int) {
	s.conns[i], s.conns[j] = s.conns[j], s.conns[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}
