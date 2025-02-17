// Package bep0051 implements DHT infohash indexing https://www.bittorrent.org/beps/bep_0051.html
package bep0051

import (
	"context"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/v2"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

const (
	Query  = "sample_infohashes"
	TTLMin = 0
	TTLMax = 21600
)

type Args struct {
	ID     krpc.ID `bencode:"id"`               // ID of the querying Node
	Target krpc.ID `bencode:"target,omitempty"` // ID of the node sought
}

type Request struct {
	Q string `bencode:"q,omitempty"` // sample_infohashes
	A Args   `bencode:"a,omitempty"`
	T string `bencode:"t"` // required: transaction ID
	Y string `bencode:"y"` // required: type of the message: q for QUERY, r for RESPONSE, e for ERROR
}

type Sample struct {
	ID        krpc.ID                  `bencode:"id"`               // ID of the sending node.
	Interval  uint                     `bencode:"interval"`         // ttl for the current sample to refresh.
	Available uint                     `bencode:"num"`              // total number of info hashes available for this node.
	Nodes     krpc.CompactIPv4NodeInfo `bencode:"nodes,omitempty"`  // K closest nodes to the requested target
	Nodes6    krpc.CompactIPv6NodeInfo `bencode:"nodes6,omitempty"` // K closest nodes to the requested target
	Sample    []byte                   `bencode:"samples"`          // sample infohashes
}

type Response struct {
	R Sample `bencode:"r"` // required sample info hashes
	T string `bencode:"t"` // required: transaction ID
	Y string `bencode:"y"` // required: type of the message: r for RESPONSE, e for ERROR
}

func NewRequest(from krpc.ID, to krpc.ID) Request {
	return Request{
		T: krpc.TimestampTransactionID(),
		Y: "q",
		Q: Query,
		A: Args{
			ID:     from,
			Target: to,
		},
	}
}

func NewRequestBinary(from krpc.ID, to krpc.ID) (Request, []byte, error) {
	req := NewRequest(from, to)
	encoded, err := bencode.Marshal(req)
	return req, encoded, err
}

type Sampler interface {
	Snapshot(max int) (ttl uint, total uint, sample []byte)
}

// provide a noop implementation that returns no hashes.
type EmptySampler struct{}

func (t EmptySampler) Snapshot(max int) (ttl uint, total uint, sample []byte) {
	return TTLMax, 0, []byte{}
}

func NewEndpoint(s Sampler) Endpoint {
	return Endpoint{s: s}
}

type Endpoint struct {
	s Sampler
}

func (t Endpoint) Handle(ctx context.Context, source dht.Addr, s *dht.Server, raw []byte, _ *krpc.Msg) error {
	var (
		m Request
	)

	if err := bencode.Unmarshal(raw, &m); err != nil {
		return err
	}

	ttl, total, sampled := t.s.Snapshot(128)
	msg := Response{
		T: m.T,
		R: Sample{
			ID:        s.ID(),
			Interval:  ttl,
			Available: total,
			Sample:    sampled,
			Nodes:     s.MakeReturnNodes(dht.Int160FromByteArray(m.A.Target), func(na krpc.NodeAddr) bool { return na.IP.Is4() }),
			Nodes6:    s.MakeReturnNodes(dht.Int160FromByteArray(m.A.Target), func(krpc.NodeAddr) bool { return true }),
		},
	}

	b, err := bencode.Marshal(msg)
	if err != nil {
		return err
	}

	_, err = s.SendToNode(ctx, b, source, false)
	return err
}
