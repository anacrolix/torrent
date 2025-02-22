package dht

import (
	"context"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

func NewFindRequest(from krpc.ID, id krpc.ID) (query, tid string, b []byte, err error) {
	return NewMessageRequest(
		"find_node",
		from,
		&krpc.MsgArgs{
			Target: id,
			Want:   []krpc.Want{krpc.WantNodes, krpc.WantNodes6},
		},
	)
}

func FindNode(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID) (m krpc.Msg, writes numWrites, err error) {
	query, tid, encoded, err := NewFindRequest(from, id)
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

// Sends a find_node query to addr. targetID is the node we're looking for.
func (s *Server) findNode(ctx context.Context, addr Addr, targetID Int160) (m krpc.Msg, writes numWrites, err error) {
	m, writes, err = FindNode(ctx, s, addr, s.ID(), targetID.bits)
	// Scrape peers from the response to put in the server's table before
	// handing the response back to the caller.
	s.mu.Lock()
	s.addResponseNodes(m)
	s.mu.Unlock()
	return m, writes, err
}
