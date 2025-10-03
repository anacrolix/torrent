package torrent

import (
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

// A non-dynamic way to register handlers to run just once when the client is unlocked.
type clientUnlockHandlers struct {
	updateNextAnnounces map[*Torrent]struct{}
}

func (me *clientUnlockHandlers) deferUpdateTrackerNextAnnounceValues(t *Torrent) {
	g.MakeMapIfNil(&me.updateNextAnnounces)
	if g.MapInsert(me.updateNextAnnounces, t, struct{}{}).Ok {
		torrent.Add("dedupedUpdateTrackerNextAnnounceValues", 1)
	}
}

func (me *clientUnlockHandlers) run() {
	for t := range me.updateNextAnnounces {
		t.updateTrackerNextAnnounceValues()
		delete(me.updateNextAnnounces, t)
	}
	panicif.NotEq(len(me.updateNextAnnounces), 0)
}
