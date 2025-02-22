package dht

import (
	"context"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

func NewPeersRequest(from krpc.ID, id krpc.ID) (query, tid string, b []byte, err error) {
	return NewMessageRequest(
		"get_peers",
		from,
		&krpc.MsgArgs{
			Target: id,
			Want:   []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
		},
	)
}

func FindPeers(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID) (m krpc.Msg, writes numWrites, err error) {
	query, tid, encoded, err := NewPeersRequest(from, id)
	if err != nil {
		return m, writes, err
	}

	encoded, writes, err = q.QueryContext(ctx, to, query, tid, encoded)
	if err != nil {
		return m, writes, err
	}

	if err = bencode.Unmarshal(encoded, &m); err != nil {
		return m, writes, err
	}

	return m, writes, nil
}
