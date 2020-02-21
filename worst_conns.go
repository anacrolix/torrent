package torrent

import (
	"container/heap"
	"fmt"
	"unsafe"

	"github.com/anacrolix/multiless"
)

func worseConn(l, r *PeerConn) bool {
	less, ok := multiless.New().Bool(
		l.useful(), r.useful()).CmpInt64(
		l.lastHelpful().Sub(r.lastHelpful()).Nanoseconds()).CmpInt64(
		l.completedHandshake.Sub(r.completedHandshake).Nanoseconds()).Uint32(
		l.peerPriority(), r.peerPriority()).Uintptr(
		uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(r))).LessOk()
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
	return worseConn(me.conns[i], me.conns[j])
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
