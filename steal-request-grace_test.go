package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

// torrentWithRequestAge builds the minimum Torrent that stealRequestGraceElapsed reads: a
// config carrying the grace, and one request state whose issue time is age in the past.
func torrentWithRequestAge(grace, age time.Duration) (*Torrent, RequestIndex) {
	const req RequestIndex = 7
	return &Torrent{
		cl: &Client{config: &ClientConfig{StealRequestGrace: grace}},
		requestState: map[RequestIndex]requestState{
			req: {when: time.Now().Add(-age)},
		},
	}, req
}

func TestStealRequestGraceElapsed(t *testing.T) {
	for _, c := range []struct {
		name  string
		grace time.Duration
		age   time.Duration
		want  bool
	}{
		// Zero and negative are both "no grace", so a client that never sets the field keeps
		// the unconditional stealing this guard was added to bound.
		{"disabled steals a brand new request", 0, 0, true},
		{"negative is treated as disabled", -time.Second, 0, true},
		// The point of the change: a request its holder has not had time to answer stays put.
		{"younger than the grace is not stealable", time.Second, time.Millisecond, false},
		{"just under the grace is not stealable", time.Second, 999 * time.Millisecond, false},
		// And the load balancing the grace is bounding still happens, just later.
		{"at the grace is stealable", time.Second, time.Second, true},
		{"older than the grace is stealable", time.Second, time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			tor, req := torrentWithRequestAge(c.grace, c.age)
			qt.Check(t, qt.Equals(tor.stealRequestGraceElapsed(req), c.want))
		})
	}
}

// The grace has to survive a steal, or a request could hop from peer to peer indefinitely as
// long as each hop happened within one grace of the original request. PeerConn.request rewrites
// requestState.when on every issue, which is what makes the grace per-holder.
func TestStealRequestGraceIsPerHolderNotPerRequest(t *testing.T) {
	tor, req := torrentWithRequestAge(50*time.Millisecond, time.Hour)
	qt.Assert(t, qt.IsTrue(tor.stealRequestGraceElapsed(req)))

	// Stand in for the re-issue in PeerConn.request that follows the steal.
	tor.requestState[req] = requestState{when: time.Now()}
	qt.Check(t, qt.IsFalse(tor.stealRequestGraceElapsed(req)))
}

func TestNewDefaultClientConfigStealRequestGrace(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		// Explicitly empty rather than merely unset: the suite is also run with the variable
		// exported, to check a grace does not stall the end-to-end download tests.
		t.Setenv(stealRequestGraceEnvKey, "")
		qt.Check(t, qt.Equals(NewDefaultClientConfig().StealRequestGrace, defaultStealRequestGrace))
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv(stealRequestGraceEnvKey, "250ms")
		qt.Check(t, qt.Equals(NewDefaultClientConfig().StealRequestGrace, 250*time.Millisecond))
	})
	// The control arm of a sweep is an explicit zero, so it has to be distinguishable from
	// "unset" rather than falling through to the default.
	t.Run("env override can select the control arm", func(t *testing.T) {
		t.Setenv(stealRequestGraceEnvKey, "0s")
		qt.Check(t, qt.Equals(NewDefaultClientConfig().StealRequestGrace, 0))
	})
	// A typo in a sweep must not quietly produce another run of the default arm.
	t.Run("malformed env value panics", func(t *testing.T) {
		t.Setenv(stealRequestGraceEnvKey, "250")
		qt.Check(t, qt.PanicMatches(func() { NewDefaultClientConfig() }, ".*missing unit.*"))
	})
}
