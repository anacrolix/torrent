package torrent

import (
	"log/slog"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type torrentUnlockActions struct {
	updateComplete bool
}

// A non-dynamic way to register handlers to run just once when the client is unlocked.
type clientUnlockHandlers struct {
	torrentActions     map[*Torrent]torrentUnlockActions
	changedPieceStates map[*Piece]struct{}
}

func (me *clientUnlockHandlers) init() {
	g.MakeMap(&me.torrentActions)
	g.MakeMap(&me.changedPieceStates)
}

func (me *clientUnlockHandlers) addUpdateComplete(t *Torrent) {
	v := me.torrentActions[t]
	v.updateComplete = true
	me.torrentActions[t] = v
}

func (me *clientUnlockHandlers) run(logger *slog.Logger) {
	started := time.Now()
	for t, v := range me.torrentActions {
		if v.updateComplete {
			t.updateComplete()
		}
		delete(me.torrentActions, t)
	}
	since := time.Since(started)
	// Around here the Go scheduler starts to do crazy stuff.
	if since > 20*time.Millisecond {
		logger.Warn("client unlock handlers took a long time", "duration", since)
	}
	for p := range me.changedPieceStates {
		p.publishStateChange()
		delete(me.changedPieceStates, p)
	}
	panicif.NotEq(len(me.torrentActions), 0)
}
