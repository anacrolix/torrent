package torrent

import (
	"time"
)

// Implements heap functions such that [0] is the worst connection.
type worstConns struct {
	c []*connection
	t *torrent
}

func (me worstConns) Len() int      { return len(me.c) }
func (me worstConns) Swap(i, j int) { me.c[i], me.c[j] = me.c[j], me.c[i] }

func (me *worstConns) Pop() (ret interface{}) {
	old := me.c
	n := len(old)
	ret = old[n-1]
	me.c = old[:n-1]
	return
}

func (me *worstConns) Push(x interface{}) {
	me.c = append(me.c, x.(*connection))
}

type worstConnsSortKey struct {
	level int
	age   time.Duration
}

func (me worstConnsSortKey) Less(other worstConnsSortKey) bool {
	if me.level != other.level {
		return me.level > other.level
	}
	return me.age > other.age
}

func (me worstConns) key(i int) (key worstConnsSortKey) {
	c := me.c[i]
	if time.Now().Sub(c.completedHandshake) >= 30*time.Second && !me.t.connHasWantedPieces(c) {
		key.level = 1
	}
	key.age = time.Duration(1+3*c.UnwantedChunksReceived) * time.Now().Sub(func() time.Time {
		if !c.lastUsefulChunkReceived.IsZero() {
			return c.lastUsefulChunkReceived
		}
		return c.completedHandshake.Add(-time.Minute)
	}()) / time.Duration(1+c.UsefulChunksReceived)
	return
}

func (me worstConns) Less(i, j int) bool {
	return me.key(i).Less(me.key(j))
}
