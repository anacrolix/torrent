package dht

import (
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

// func transactionSender(
// 	ctx context.Context,
// 	send func(context.Context) error,
// 	resendDelay func() time.Duration,
// ) error {
// 	return send(ctx)
// 	// const maxTransactionSends = 5
// 	// var delay time.Duration
// 	// sends := 0
// 	// for sends < maxTransactionSends {
// 	// 	select {
// 	// 	case <-time.After(delay):
// 	// 		if err := send(ctx); err != nil {
// 	// 			return err
// 	// 		}
// 	// 		sends++
// 	// 		delay = resendDelay()
// 	// 	case <-ctx.Done():
// 	// 		return ctx.Err()
// 	// 	}
// 	// }

// 	// return nil
// }
