package dht

import (
	"context"

	"github.com/james-lawrence/torrent/dht/krpc"
)

func NewFindRequest(from krpc.ID, id krpc.ID, want []krpc.Want) (qi QueryInput, err error) {
	return NewMessageRequest(
		"find_node",
		from,
		&krpc.MsgArgs{
			Target: id,
			Want:   want,
		},
	)
}

func FindNode(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID, want []krpc.Want) QueryResult {
	query, err := NewFindRequest(from, id, want)
	if err != nil {
		return NewQueryResultErr(err)
	}

	return q.Query(ctx, to, query)
}
