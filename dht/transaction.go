package dht

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/james-lawrence/torrent/dht/krpc"
	"github.com/james-lawrence/torrent/internal/errorsx"
)

var ErrTransactionTimeout = errors.New("transaction timed out")
var ErrTokenInvalid = errors.New("invalid token")

const (
	ErrDHTNoInitialNodes = errorsx.String("no initial nodes")
)

// Transaction keeps track of a message exchange between nodes, such as a
// query message and a response message.
type transaction struct {
	onResponse func([]byte, krpc.Msg)
}

func (t *transaction) handleResponse(raw []byte, m krpc.Msg) {
	t.onResponse(raw, m)
}

type socket interface {
	WriteTo(b []byte, addr net.Addr) (int, error)
}

func repeatsend(
	ctx context.Context,
	s socket,
	to net.Addr,
	b []byte,
	delay time.Duration,
	maximum int,
) (n int, last error) {
	if maximum == 0 {
		log.Println("warning: repeat send called with a maximum of 0 attemtps, this is a bug by client code, but adjusting to default attempts.")
		maximum = defaultAttempts
	}
	var d time.Duration
	for attempts := 0; attempts < maximum; attempts++ {
		select {
		case <-time.After(d):
			n, last = s.WriteTo(b, to)
			d = delay
		case <-ctx.Done():
			return n, last
		}
	}

	if last != nil {
		return n, fmt.Errorf("error writing %d bytes to %s: %v", len(b), to, last)
	}

	return n, nil
}
