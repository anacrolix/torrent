//go:build go1.25

package torrent

import (
	"log/slog"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	analog "github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/go-quicktest/qt"
)

// This test doesn't really do much useful anymore. It is useful to break apart the dispatcher a bit
// for testing. It's good to have something that hits up the triggers a bit.
func TestUpdateOverdueRecursion(t *testing.T) {
	// Prevent synctest from tracking some stuff that we don't care about.
	cl := newTestingClient(t)
	synctest.Test(t, func(t *testing.T) {
		d := regularTrackerAnnounceDispatcher{}
		d.initTables()
		d.initTimerNoop()
		d.logger = slog.Default()
		u, _ := url.Parse("http://derp")
		d.initTrackerClient(u, trackerAnnouncerKey(u.String()), cl.config, analog.Logger{})
		// Two values. One that needs to be marked not overdue on the first call to updateOverdue,
		// and the other that is by a recursive call, and subsequently reversed when we bounce back
		// out to the original call.
		key1 := torrentTrackerAnnouncerKey{}
		key1.ShortInfohash[0] = 1
		key2 := torrentTrackerAnnouncerKey{}
		key2.ShortInfohash[0] = 2
		value1 := nextAnnounceInput{}
		value1.overdue = false
		value1.When = time.Now()
		value2 := nextAnnounceInput{}
		value2.overdue = true
		value2.When = time.Now().Add(4)
		println(value1.When.UnixNano(), value2.When.UnixNano())
		panicif.False(d.announceData.Create(key1, value1))
		panicif.False(d.announceData.Create(key2, value2))
		v2, ok := d.announceData.Get(key2)
		panicif.False(ok)
		expectedValue2 := value2
		expectedValue2.overdue = false
		qt.Check(t, qt.Equals(v2, expectedValue2))
		println(time.Now().UnixNano())
		// This will fix up the values. But if we can advance time and trigger a recursive
		// updateOverdue we can test for thrashing, but it's non-trivial.
		d.updateOverdue()
	})
}
