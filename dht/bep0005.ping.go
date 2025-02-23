package dht

import (
	"context"
	"time"

	"github.com/james-lawrence/torrent/dht/krpc"
)

func NewPingRequest(from krpc.ID) (qi QueryInput, err error) {
	return NewMessageRequest(
		"ping",
		from,
		&krpc.MsgArgs{},
	)
}

func Ping(ctx context.Context, q Queryer, to Addr, from krpc.ID) QueryResult {
	qi, err := NewPingRequest(from)
	if err != nil {
		return NewQueryResultErr(err)
	}

	return q.Query(ctx, to, qi)
}

func Ping3S(ctx context.Context, q Queryer, to Addr, from krpc.ID) QueryResult {
	ctx, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	return Ping(ctx, q, to, from)
}
