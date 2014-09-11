package torrent

import (
	"time"
)

// Implements heap functions such that [0] is the worst connection.
type worstConns []*connection

func (me worstConns) Len() int      { return len(me) }
func (me worstConns) Swap(i, j int) { me[i], me[j] = me[j], me[i] }

func (me *worstConns) Pop() (ret interface{}) {
	old := *me
	n := len(old)
	ret = old[n-1]
	*me = old[:n-1]
	return
}

func (me *worstConns) Push(x interface{}) {
	*me = append(*me, x.(*connection))
}

func (me worstConns) key(i int) (ret time.Duration) {
	c := me[i]
	return time.Duration(1+c.UnwantedChunksReceived) * time.Now().Sub(func() time.Time {
		if !c.lastUsefulChunkReceived.IsZero() {
			return c.lastUsefulChunkReceived
		}
		return c.completedHandshake.Add(-time.Minute)
	}()) / time.Duration(1+c.UsefulChunksReceived)
}

func (me worstConns) Less(i, j int) bool {
	return me.key(i) > me.key(j)
}
