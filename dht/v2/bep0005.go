package dht

import (
	"context"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

func NewMessageRequest(q string, from krpc.ID, a *krpc.MsgArgs) (query, tid string, b []byte, err error) {
	t := krpc.TimestampTransactionID()
	m := krpc.Msg{
		T: t,
		Y: "q",
		Q: q,
		A: a,
	}

	b, err = bencode.Marshal(m)
	return q, t, b, err
}

type Queryer interface {
	QueryContext(ctx context.Context, addr Addr, q string, tid string, msg []byte) (reply []byte, writes numWrites, err error)
}
