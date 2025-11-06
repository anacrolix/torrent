package torrent

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

type torrentUnlockActions struct {
	updateRegularTrackerAnnouncing bool
	updateComplete                 bool
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

func (me *clientUnlockHandlers) deferUpdateTorrentRegularTrackerAnnouncing(t *Torrent) {
	g.MakeMapIfNil(&me.torrentActions)
	value := me.torrentActions[t]
	value.updateRegularTrackerAnnouncing = true
	me.torrentActions[t] = value
}

func (me *clientUnlockHandlers) addUpdateComplete(t *Torrent) {
	v := me.torrentActions[t]
	v.updateComplete = true
	me.torrentActions[t] = v
}

func (me *clientUnlockHandlers) run() {
	for t, v := range me.torrentActions {
		if v.updateRegularTrackerAnnouncing {
			t.updateRegularTrackerAnnouncing()
		}
		if v.updateComplete {
			t.updateComplete()
		}
		delete(me.torrentActions, t)
	}
	panicif.NotEq(len(me.torrentActions), 0)
	for p := range me.changedPieceStates {
		p.publishStateChange()
		delete(me.changedPieceStates, p)
	}
}
