package dht

import (
	"context"

	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/dht/krpc"
)

func NewFindRequest(from krpc.ID, id int160.T, want []krpc.Want) (qi QueryInput, err error) {
	return NewMessageRequest(
		"find_node",
		from,
		&krpc.MsgArgs{
			Target: id.AsByteArray(),
			Want:   want,
		},
	)
}

func FindNode(ctx context.Context, q Queryer, to Addr, from krpc.ID, id int160.T, want []krpc.Want) QueryResult {
	query, err := NewFindRequest(from, id, want)
	if err != nil {
		return NewQueryResultErr(err)
	}

	return q.Query(ctx, to, query)
}

// locates the nearest peer.
type BEP0005FindNode struct{}

func (t BEP0005FindNode) Handle(ctx context.Context, source Addr, s *Server, raw []byte, m *krpc.Msg) error {
	// case "find_node":
	// 	var r krpc.Return
	// 	if err := s.setReturnNodes(&r, m, source); err != nil {
	// 		s.sendError(source, m.T, *err)
	// 		break
	// 	}
	// 	s.reply(source, m.T, r)
	var r krpc.Return
	if err := s.setReturnNodes(&r, *m, source); err != nil {
		return s.sendError(ctx, source, m.T, *err)
	}
	return s.reply(ctx, source, m.T, r)
}
