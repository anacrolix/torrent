package torrent

import (
	"time"
)

// Implements heap functions such that [0] is the worst connection.
type worstConns struct {
	c  []*connection
	t  *Torrent
	cl *Client
}

func (wc *worstConns) Len() int      { return len(wc.c) }
func (wc *worstConns) Swap(i, j int) { wc.c[i], wc.c[j] = wc.c[j], wc.c[i] }

func (wc *worstConns) Pop() (ret interface{}) {
	old := wc.c
	n := len(old)
	ret = old[n-1]
	wc.c = old[:n-1]
	return
}

func (wc *worstConns) Push(x interface{}) {
	wc.c = append(wc.c, x.(*connection))
}

type worstConnsSortKey struct {
	useful      bool
	lastHelpful time.Time
	connected   time.Time
}

func (wc worstConnsSortKey) Less(other worstConnsSortKey) bool {
	if wc.useful != other.useful {
		return !wc.useful
	}
	if !wc.lastHelpful.Equal(other.lastHelpful) {
		return wc.lastHelpful.Before(other.lastHelpful)
	}
	return wc.connected.Before(other.connected)
}

func (wc *worstConns) key(i int) (key worstConnsSortKey) {
	c := wc.c[i]
	key.useful = wc.cl.usefulConn(wc.t, c)
	if wc.t.seeding() {
		key.lastHelpful = c.lastChunkSent
	}
	// Intentionally consider the last time a chunk was received when seeding,
	// because we might go from seeding back to leeching.
	if c.lastUsefulChunkReceived.After(key.lastHelpful) {
		key.lastHelpful = c.lastUsefulChunkReceived
	}
	key.connected = c.completedHandshake
	return
}

func (wc worstConns) Less(i, j int) bool {
	return wc.key(i).Less(wc.key(j))
}
