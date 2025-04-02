package dht

import (
	"context"
	"log"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/dht/bep44"
	"github.com/james-lawrence/torrent/dht/krpc"
	peer_store "github.com/james-lawrence/torrent/dht/peer-store"
	"github.com/james-lawrence/torrent/internal/langx"
	"github.com/james-lawrence/torrent/metainfo"
)

// Standard Muxer configuration used by the server.
func DefaultMuxer() Muxer {
	m := NewMuxer()
	m.Method("ping", HandlerPing{})
	m.Method("get_peers", HandlerPeers{})
	m.Method("find_node", BEP0005FindNode{})
	m.Method("announce_peer", HandlerAnnounce{})
	// m.Method("put", Bep44Put{})
	// m.Method("get", Bep44Get{})
	return m
}

func NewMuxer() Muxer {
	return defaultMuxer{
		m:        make(map[string]Handler, 10),
		fallback: UnimplementedHandler{},
	}
}

type defaultMuxer struct {
	m        map[string]Handler
	fallback Handler
}

func (t defaultMuxer) Method(name string, fn Handler) Muxer {
	t.m[name] = fn
	return t
}

func (t defaultMuxer) Handler(raw []byte, r *krpc.Msg) (pattern string, fn Handler) {
	if fn, ok := t.m[r.Q]; ok {
		return r.Q, fn
	}

	return r.Q, t.fallback
}

type Handler interface {
	Handle(ctx context.Context, src Addr, srv *Server, raw []byte, msg *krpc.Msg) error
}

type Muxer interface {
	Method(name string, fn Handler) Muxer
	Handler(raw []byte, r *krpc.Msg) (pattern string, fn Handler)
}

type UnimplementedHandler struct{}

func (t UnimplementedHandler) Handle(ctx context.Context, source Addr, s *Server, raw []byte, m *krpc.Msg) error {
	log.Println("unimplemented rpc method was received", m.Q, source.String())
	// TODO: http://libtorrent.org/dht_extensions.html#forward-compatibility
	return krpc.ErrorMethodUnknown
}

type HandlerPing struct{}

func (t HandlerPing) Handle(ctx context.Context, src Addr, srv *Server, raw []byte, msg *krpc.Msg) error {
	return srv.reply(ctx, src, msg.T, krpc.Return{})
}

type HandlerPeers struct{}

func (t HandlerPeers) Handle(ctx context.Context, src Addr, srv *Server, raw []byte, msg *krpc.Msg) error {
	var r krpc.Return

	// Check for the naked m.A.Want deref below.
	if msg.A == nil {
		return srv.sendError(ctx, src, msg.T, krpcErrMissingArguments)
	}

	if ps := srv.config.PeerStore; ps != nil {
		r.Values = filterPeers(src.IP(), msg.A.Want, ps.GetPeers(peer_store.InfoHash(msg.A.InfoHash)))
		r.Token = langx.Autoptr(srv.createToken(src))
	}

	if len(r.Values) == 0 {
		if err := srv.setReturnNodes(&r, *msg, src); err != nil {
			return err
		}
	}

	return srv.reply(ctx, src, msg.T, r)
}

type HandlerAnnounce struct{}

func (t HandlerAnnounce) Handle(ctx context.Context, source Addr, s *Server, raw []byte, m *krpc.Msg) error {
	if !s.validToken(m.A.Token, source) {
		log.Println("invalid announce token received from:", source.String())
		return nil
	}

	var port int
	portOk := false
	if m.A.Port != nil {
		port = *m.A.Port
		portOk = true
	}
	if m.A.ImpliedPort {
		port = source.Port()
		portOk = true
	}

	if h := s.config.OnAnnouncePeer; h != nil {
		go h(metainfo.Hash(m.A.InfoHash), source.IP(), port, portOk)
	}

	if ps := s.config.PeerStore; ps != nil {
		go ps.AddPeer(
			peer_store.InfoHash(m.A.InfoHash),
			krpc.NewNodeAddrFromIPPort(source.IP(), port),
		)
	}

	return s.reply(ctx, source, m.T, krpc.Return{})
}

type Bep44Get struct{}

func (t Bep44Get) Handle(ctx context.Context, source Addr, s *Server, raw []byte, m *krpc.Msg) error {
	var r krpc.Return
	if err := s.setReturnNodes(&r, *m, source); err != nil {
		s.sendError(ctx, source, m.T, *err)
		return err
	}

	r.Token = langx.Autoptr(s.createToken(source))

	item, err := s.store.Get(bep44.Target(m.A.Target))
	if err == bep44.ErrItemNotFound {
		return s.reply(ctx, source, m.T, r)
	}

	if err != nil {
		return krpc.Error{
			Code: krpc.ErrorCodeGenericError,
			Msg:  err.Error(),
		}
	}

	r.Seq = &item.Seq

	if m.A.Seq != nil && item.Seq <= *m.A.Seq {
		return s.reply(ctx, source, m.T, r)
	}

	r.V = bencode.MustMarshal(item.V)
	r.K = item.K
	r.Sig = item.Sig

	return s.reply(ctx, source, m.T, r)
}

type Bep44Put struct{}

func (t Bep44Put) Handle(ctx context.Context, source Addr, s *Server, raw []byte, m *krpc.Msg) error {
	if !s.validToken(m.A.Token, source) {
		return ErrTokenInvalid
	}

	if m.A.Seq == nil {
		return krpc.Error{
			Code: krpc.ErrorCodeProtocolError,
			Msg:  "expected seq argument",
		}
	}

	i := &bep44.Item{
		V:    m.A.V,
		K:    m.A.K,
		Salt: m.A.Salt,
		Sig:  m.A.Sig,
		Cas:  m.A.Cas,
		Seq:  *m.A.Seq,
	}

	if err := s.store.Put(i); err != nil {
		kerr, ok := err.(krpc.Error)
		if !ok {
			return krpc.ErrorMethodUnknown
		}

		return kerr
	}

	return s.reply(ctx, source, m.T, krpc.Return{
		ID: s.ID(),
	})
}
