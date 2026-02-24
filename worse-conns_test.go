package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

func TestWorseConnLastHelpful(t *testing.T) {
	qt.Check(t, qt.IsTrue((&worseConnInput{}).Less(&worseConnInput{LastHelpful: time.Now()})))
	qt.Check(t, qt.IsTrue((&worseConnInput{}).Less(&worseConnInput{CompletedHandshake: time.Now()})))
	qt.Check(t, qt.IsFalse((&worseConnInput{LastHelpful: time.Now()}).Less(&worseConnInput{CompletedHandshake: time.Now()})))
	qt.Check(t, qt.IsTrue((&worseConnInput{
		LastHelpful: time.Now(),
	}).Less(&worseConnInput{
		LastHelpful:        time.Now(),
		CompletedHandshake: time.Now(),
	})))
	now := time.Now()
	qt.Check(t, qt.IsFalse((&worseConnInput{
		LastHelpful: now,
	}).Less(&worseConnInput{
		LastHelpful:        now.Add(-time.Nanosecond),
		CompletedHandshake: now,
	})))
	readyPeerPriority := func() (peerPriority, error) {
		return 42, nil
	}
	qt.Check(t, qt.IsTrue((&worseConnInput{
		GetPeerPriority: readyPeerPriority,
	}).Less(&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         1,
	})))
	qt.Check(t, qt.IsFalse((&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         2,
	}).Less(&worseConnInput{
		GetPeerPriority: readyPeerPriority,
		Pointer:         1,
	})))
}
