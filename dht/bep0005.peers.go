package dht

import (
	"context"

	"github.com/james-lawrence/torrent/dht/krpc"
)

func NewPeersRequest(from krpc.ID, id krpc.ID, scrape bool) (qi QueryInput, err error) {
	scrapeint := 0
	if scrape {
		scrapeint = 1
	}
	return NewMessageRequest(
		"get_peers",
		from,
		&krpc.MsgArgs{
			Target:   id,
			InfoHash: id,
			Scrape:   scrapeint,
			Want:     []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
		},
	)
}

func FindPeers(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID, scrape bool) (ret QueryResult) {
	qi, err := NewPeersRequest(from, id, scrape)
	if err != nil {
		return NewQueryResultErr(err)
	}

	return q.Query(ctx, to, qi)
}
