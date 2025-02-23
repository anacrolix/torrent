package dht

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/james-lawrence/torrent/dht/krpc"
)

var ErrTransactionTimeout = errors.New("transaction timed out")
var ErrTokenInvalid = errors.New("invalid token")

// Transaction keeps track of a message exchange between nodes, such as a
// query message and a response message.
type transaction struct {
	onResponse func([]byte, krpc.Msg)
}

func (t *transaction) handleResponse(raw []byte, m krpc.Msg) {
	t.onResponse(raw, m)
}

const defaultMaxQuerySends = 1

func transactionSender(
	ctx context.Context,
	send func() error,
	resendDelay func() time.Duration,
	maxSends int,
) error {
	var delay time.Duration
	sends := 0
	for sends < maxSends {
		select {
		case <-time.After(delay):
			err := send()
			sends++
			if err != nil {
				return fmt.Errorf("send %d: %w", sends, err)
			}
			delay = resendDelay()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
