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
	// Peer has something we want.
	useless bool
	// A fabricated duration since peer was last helpful.
	age time.Duration
}

func (me worstConnsSortKey) Less(other worstConnsSortKey) bool {
	if me.useless != other.useless {
		return me.useless
	}
	return me.age > other.age
}

func (me worstConns) key(i int) (key worstConnsSortKey) {
	c := me.c[i]
	// Peer has had time to declare what they have.
	if time.Now().Sub(c.completedHandshake) >= 30*time.Second {
		if !me.t.haveInfo() {
			if _, ok := c.PeerExtensionIDs["ut_metadata"]; !ok {
				key.useless = true
			}
		} else {
			if !me.t.connHasWantedPieces(c) {
				key.useless = true
			}
		}
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
