package tracker

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/missinggo/v2"

	"github.com/anacrolix/torrent/tracker/udp"
)

type torrent struct {
	Leechers int32
	Seeders  int32
	Peers    []krpc.NodeAddr
}

type server struct {
	pc    net.PacketConn
	conns map[udp.ConnectionId]struct{}
	t     map[[20]byte]torrent
}

func marshal(parts ...interface{}) (ret []byte, err error) {
	var buf bytes.Buffer
	for _, p := range parts {
		err = binary.Write(&buf, binary.BigEndian, p)
		if err != nil {
			return
		}
	}
	ret = buf.Bytes()
	return
}

func (s *server) respond(addr net.Addr, rh udp.ResponseHeader, parts ...interface{}) (err error) {
	b, err := marshal(append([]interface{}{rh}, parts...)...)
	if err != nil {
		return
	}
	_, err = s.pc.WriteTo(b, addr)
	return
}

func (s *server) newConn() (ret udp.ConnectionId) {
	ret = rand.Uint64()
	if s.conns == nil {
		s.conns = make(map[udp.ConnectionId]struct{})
	}
	s.conns[ret] = struct{}{}
	return
}

func (s *server) serveOne() (err error) {
	b := make([]byte, 0x10000)
	n, addr, err := s.pc.ReadFrom(b)
	if err != nil {
		return
	}
	r := bytes.NewReader(b[:n])
	var h udp.RequestHeader
	err = udp.Read(r, &h)
	if err != nil {
		return
	}
	switch h.Action {
	case udp.ActionConnect:
		if h.ConnectionId != udp.ConnectRequestConnectionId {
			return
		}
		connId := s.newConn()
		err = s.respond(addr, udp.ResponseHeader{
			udp.ActionConnect,
			h.TransactionId,
		}, udp.ConnectionResponse{
			connId,
		})
		return
	case udp.ActionAnnounce:
		if _, ok := s.conns[h.ConnectionId]; !ok {
			s.respond(addr, udp.ResponseHeader{
				TransactionId: h.TransactionId,
				Action:        udp.ActionError,
			}, []byte("not connected"))
			return
		}
		var ar AnnounceRequest
		err = udp.Read(r, &ar)
		if err != nil {
			return
		}
		t := s.t[ar.InfoHash]
		bm := func() encoding.BinaryMarshaler {
			ip := missinggo.AddrIP(addr)
			if ip.To4() != nil {
				return krpc.CompactIPv4NodeAddrs(t.Peers)
			}
			return krpc.CompactIPv6NodeAddrs(t.Peers)
		}()
		b, err = bm.MarshalBinary()
		if err != nil {
			panic(err)
		}
		err = s.respond(addr, udp.ResponseHeader{
			TransactionId: h.TransactionId,
			Action:        udp.ActionAnnounce,
		}, udp.AnnounceResponseHeader{
			Interval: 900,
			Leechers: t.Leechers,
			Seeders:  t.Seeders,
		}, b)
		return
	default:
		err = fmt.Errorf("unhandled action: %d", h.Action)
		s.respond(addr, udp.ResponseHeader{
			TransactionId: h.TransactionId,
			Action:        udp.ActionError,
		}, []byte("unhandled action"))
		return
	}
}
