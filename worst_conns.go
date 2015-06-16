package torrent

import (
	"time"
)

// Implements heap functions such that [0] is the worst connection.
type worstConns struct {
	c  []*connection
	t  *torrent
	cl *Client
}

func (me *worstConns) Len() int      { return len(me.c) }
func (me *worstConns) Swap(i, j int) { me.c[i], me.c[j] = me.c[j], me.c[i] }

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
	useful      bool
	lastHelpful time.Time
}

func (me worstConnsSortKey) Less(other worstConnsSortKey) bool {
	if me.useful != other.useful {
		return !me.useful
	}
	return me.lastHelpful.Before(other.lastHelpful)
}

func (me *worstConns) key(i int) (key worstConnsSortKey) {
	c := me.c[i]
	key.useful = me.cl.usefulConn(me.t, c)
	if me.cl.seeding(me.t) {
		key.lastHelpful = c.lastChunkSent
	} else {
		key.lastHelpful = c.lastUsefulChunkReceived
	}
	return
}

func (me worstConns) Less(i, j int) bool {
	return me.key(i).Less(me.key(j))
}
