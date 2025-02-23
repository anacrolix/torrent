package dht

import (
	"context"
	"errors"

	"github.com/james-lawrence/torrent/dht/krpc"
)

func NewAnnouncePeerRequest(from krpc.ID, id krpc.ID, port int, token string, impliedPort bool) (qi QueryInput, err error) {
	if port == 0 && !impliedPort {
		err = errors.New("no port specified")
		return
	}
	return NewMessageRequest(
		"announce_peer",
		from,
		&krpc.MsgArgs{
			ImpliedPort: impliedPort,
			InfoHash:    id,
			Port:        &port,
			Token:       token,
		},
	)
}

func AnnouncePeerQuery(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID, port int, token string, impliedPort bool) QueryResult {
	qi, err := NewAnnouncePeerRequest(from, id, port, token, impliedPort)
	if err != nil {
		return NewQueryResultErr(err)
	}

	return q.Query(ctx, to, qi)
}
