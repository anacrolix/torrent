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

// Sends a ping query to the address given.
// -func (s *Server) Ping(node *net.UDPAddr, callback func(krpc.Msg, error)) error {
// 	-	return s.ping(node, callback)
// 	-}
// 	-
// 	-func (s *Server) ping(node *net.UDPAddr, callback func(krpc.Msg, error)) error {
// 	-	return s.query(context.Background(), NewAddr(node), "ping", nil, func(b []byte, err error) {
// 	-		if callback == nil {
// 	-			return
// 	-		}
// 	-
// 	-		var m krpc.Msg
// 			 if err != nil {
// 	-			callback(m, err)
// 	-			return
// 	+			for _, n := range s.table.addrNodes(addr) {
// 	+				n.consecutiveFailures++
// 	+			}
// 			 }
// 	-		err = bencode.Unmarshal(b, &m)
// 	-		callback(m, err)
// 	-	})
// 	-}
