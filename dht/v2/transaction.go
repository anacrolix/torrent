package dht

import (
	"context"
	"time"

	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

// Transaction keeps track of a message exchange between nodes, such as a
// query message and a response message.
type Transaction struct {
	onResponse func([]byte, krpc.Msg)
}

func (t *Transaction) handleResponse(b []byte, m krpc.Msg) {
	t.onResponse(b, m)
}

const maxTransactionSends = 3

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
			if err != nil {
				return err
			}
			sends++
			delay = resendDelay()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
