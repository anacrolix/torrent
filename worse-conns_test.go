package torrent

import (
	"errors"
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

// TestWorseConnPriorityError verifies that a left-side peer-priority error still falls back to
// pointer ordering without forcing the right-side priority lookup.
func TestWorseConnPriorityError(t *testing.T) {
	rightCalls := 0
	left := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			return 0, errPriorityLookup
		},
		Pointer: 1,
	}
	right := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			rightCalls++
			return 42, nil
		},
		Pointer: 2,
	}
	qt.Check(t, qt.IsTrue(left.Less(&right)))
	qt.Check(t, qt.Equals(rightCalls, 0))
}

// TestWorseConnSameInputPanic verifies that the comparison still panics when every ordering field,
// including the pointer tie-breaker, is identical.
func TestWorseConnSameInputPanic(t *testing.T) {
	input := worseConnInput{
		GetPeerPriority: func() (peerPriority, error) {
			return 42, nil
		},
	}
	qt.Check(t, qt.PanicMatches(func() {
		input.Less(&input)
	}, "cannot differentiate.*"))
}

var errPriorityLookup = errors.New("priority lookup failed")
