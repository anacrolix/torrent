package torrent

import (
	"time"
)

type worstConnsHeap []*connection

func (me worstConnsHeap) Len() int      { return len(me) }
func (me worstConnsHeap) Swap(i, j int) { me[i], me[j] = me[j], me[i] }
func (me worstConnsHeap) last(c *connection) (ret time.Time) {
	ret = c.lastUsefulChunkReceived
	if !ret.IsZero() {
		return
	}
	ret = c.completedHandshake
	if time.Now().Sub(ret) >= 3*time.Minute {
		return
	}
	ret = time.Now().Add(-3 * time.Minute)
	return
}
func (me worstConnsHeap) Less(i, j int) bool {
	return me.last(me[i]).Before(me.last(me[j]))
}

func (me *worstConnsHeap) Pop() (ret interface{}) {
	old := *me
	n := len(old)
	ret = old[n-1]
	*me = old[:n-1]
	return
}

func (me *worstConnsHeap) Push(x interface{}) {
	*me = append(*me, x.(*connection))
}
