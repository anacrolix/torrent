package torrent

import (
	"context"
)

type dialPool struct {
	resCh chan DialResult
	addr  string
	left  int
}

func (me *dialPool) getFirst() (res DialResult) {
	for me.left > 0 && res.Conn == nil {
		res = <-me.resCh
		me.left--
	}
	return
}

func (me *dialPool) add(ctx context.Context, dialer Dialer) {
	me.left++
	go func() {
		me.resCh <- DialResult{
			dialFromSocket(ctx, dialer, me.addr),
			dialer,
		}
	}()
}

func (me *dialPool) startDrainer() {
	go me.drainAndCloseRemainingDials()
}

func (me *dialPool) drainAndCloseRemainingDials() {
	for me.left > 0 {
		conn := (<-me.resCh).Conn
		me.left--
		if conn != nil {
			conn.Close()
		}
	}
}
