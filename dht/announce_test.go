package dht

import (
	"context"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func TestAnnounceNoStartingNodes(t *testing.T) {
	s, err := NewServer(&ServerConfig{
		Conn:       mustListen(":0"),
		NoSecurity: true,
	})
	require.NoError(t, err)
	defer s.Close()
	var ih [20]byte
	copy(ih[:], "blah")
	_, err = s.Announce(ih, 0, true)
	require.EqualError(t, err, "no initial nodes")
}

func randomInfohash() (ih [20]byte) {
	rand.Read(ih[:])
	return
}

func TestAnnounceStopsNoPending(t *testing.T) {
	s, err := NewServer(&ServerConfig{
		Conn: mustListen(":0"),
		StartingNodes: func() ([]Addr, error) {
			return []Addr{NewAddr(&net.TCPAddr{})}, nil
		},
	})
	require.NoError(t, err)
	a, err := s.Announce(randomInfohash(), 0, true)
	require.NoError(t, err)
	defer a.Close()
	<-a.Peers
}

// Assert that rate.Limiter won't wake-up waiters once they have determined a
// delay. This means we can't use it to cancel reservations for queries that
// are successful.
func TestRateLimiterInadequate(t *testing.T) {
	rl := rate.NewLimiter(rate.Every(time.Hour), 1)
	assert.NoError(t, rl.Wait(context.Background()))
	time.AfterFunc(time.Millisecond, func() { rl.AllowN(time.Now(), -1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(2*time.Millisecond, cancel)
	assert.EqualValues(t, context.Canceled, rl.Wait(ctx))
}
