package dht

import (
	"context"
	"errors"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

func NewAnnouncePeerRequest(from krpc.ID, id krpc.ID, port int, token string, impliedPort bool) (query, tid string, b []byte, err error) {
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

func AnnouncePeer(ctx context.Context, q Queryer, to Addr, from krpc.ID, id krpc.ID, port int, token string, impliedPort bool) (m krpc.Msg, writes numWrites, err error) {
	query, tid, encoded, err := NewAnnouncePeerRequest(from, id, port, token, impliedPort)
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

func (s *Server) announcePeer(node Addr, infoHash Int160, port int, token string, impliedPort bool) (m krpc.Msg, writes numWrites, err error) {
	m, writes, err = AnnouncePeer(context.Background(), s, node, s.ID(), infoHash.bits, port, token, impliedPort)
	if err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stats.SuccessfulOutboundAnnouncePeerQueries++
	}

	return m, writes, err
}
