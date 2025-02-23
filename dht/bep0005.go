package dht

import (
	"context"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/krpc"
)

const defaultAttempts = 3

func NewMessageRequest(q string, from krpc.ID, a *krpc.MsgArgs) (qi QueryInput, err error) {
	var (
		encoded []byte
	)

	t := krpc.TimestampTransactionID()
	m := krpc.Msg{
		Y: "q",
		T: t,
		Q: q,
		A: a,
	}

	if encoded, err = bencode.Marshal(m); err != nil {
		return qi, err
	}

	return QueryInput{
		Method:   q,
		Tid:      t,
		Encoded:  encoded,
		NumTries: defaultAttempts,
	}, nil
}

func NewQueryResultErr(err error) QueryResult {
	return QueryResult{Err: err}
}

type Queryer interface {
	Query(ctx context.Context, addr Addr, input QueryInput) (ret QueryResult)
}
