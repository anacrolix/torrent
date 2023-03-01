package udpTrackerServer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	trackerServer "github.com/anacrolix/torrent/tracker/server"
	"github.com/anacrolix/torrent/tracker/udp"
)

type ConnectionTrackerAddr = string

type ConnectionTracker interface {
	Add(ctx context.Context, addr ConnectionTrackerAddr, id udp.ConnectionId) error
	Check(ctx context.Context, addr ConnectionTrackerAddr, id udp.ConnectionId) (bool, error)
}

type InfoHash = [20]byte

type AnnounceTracker = trackerServer.AnnounceTracker

type Server struct {
	ConnTracker  ConnectionTracker
	SendResponse func(ctx context.Context, data []byte, addr net.Addr) (int, error)
	Announce     *trackerServer.AnnounceHandler
}

type RequestSourceAddr = net.Addr

var tracer = otel.Tracer("torrent.tracker.udp")

func (me *Server) HandleRequest(
	ctx context.Context,
	family udp.AddrFamily,
	source RequestSourceAddr,
	body []byte,
) (err error) {
	ctx, span := tracer.Start(ctx, "Server.HandleRequest",
		trace.WithAttributes(attribute.Int("payload.len", len(body))))
	defer span.End()
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	var h udp.RequestHeader
	var r bytes.Reader
	r.Reset(body)
	err = udp.Read(&r, &h)
	if err != nil {
		err = fmt.Errorf("reading request header: %w", err)
		return err
	}
	switch h.Action {
	case udp.ActionConnect:
		err = me.handleConnect(ctx, source, h.TransactionId)
	case udp.ActionAnnounce:
		err = me.handleAnnounce(ctx, family, source, h.ConnectionId, h.TransactionId, &r)
	default:
		err = fmt.Errorf("unimplemented")
	}
	if err != nil {
		err = fmt.Errorf("handling action %v: %w", h.Action, err)
	}
	return err
}

func (me *Server) handleAnnounce(
	ctx context.Context,
	addrFamily udp.AddrFamily,
	source RequestSourceAddr,
	connId udp.ConnectionId,
	tid udp.TransactionId,
	r *bytes.Reader,
) error {
	// Should we set a timeout of 10s or something for the entire response, so that we give up if a
	// retry is imminent?

	ok, err := me.ConnTracker.Check(ctx, source.String(), connId)
	if err != nil {
		err = fmt.Errorf("checking conn id: %w", err)
		return err
	}
	if !ok {
		return fmt.Errorf("incorrect connection id: %x", connId)
	}
	var req udp.AnnounceRequest
	err = udp.Read(r, &req)
	if err != nil {
		return err
	}
	// TODO: This should be done asynchronously to responding to the announce.
	announceAddr, err := netip.ParseAddrPort(source.String())
	if err != nil {
		err = fmt.Errorf("converting source net.Addr to AnnounceAddr: %w", err)
		return err
	}
	opts := trackerServer.GetPeersOpts{MaxCount: generics.Some[uint](50)}
	if addrFamily == udp.AddrFamilyIpv4 {
		opts.MaxCount = generics.Some[uint](150)
	}
	res := me.Announce.Serve(ctx, req, announceAddr, opts)
	if res.Err != nil {
		return res.Err
	}
	nodeAddrs := make([]krpc.NodeAddr, 0, len(res.Peers))
	for _, p := range res.Peers {
		var ip net.IP
		switch addrFamily {
		default:
			continue
		case udp.AddrFamilyIpv4:
			if !p.Addr().Unmap().Is4() {
				continue
			}
			ipBuf := p.Addr().As4()
			ip = ipBuf[:]
		case udp.AddrFamilyIpv6:
			ipBuf := p.Addr().As16()
			ip = ipBuf[:]
		}
		nodeAddrs = append(nodeAddrs, krpc.NodeAddr{
			IP:   ip[:],
			Port: int(p.Port()),
		})
	}
	var buf bytes.Buffer
	err = udp.Write(&buf, udp.ResponseHeader{
		Action:        udp.ActionAnnounce,
		TransactionId: tid,
	})
	if err != nil {
		return err
	}
	err = udp.Write(&buf, udp.AnnounceResponseHeader{
		Interval: res.Interval.UnwrapOr(5 * 60),
		Seeders:  res.Seeders.Value,
		Leechers: res.Leechers.Value,
	})
	if err != nil {
		return err
	}
	b, err := udp.GetNodeAddrsCompactMarshaler(nodeAddrs, addrFamily).MarshalBinary()
	if err != nil {
		err = fmt.Errorf("marshalling compact node addrs: %w", err)
		return err
	}
	buf.Write(b)
	n, err := me.SendResponse(ctx, buf.Bytes(), source)
	if err != nil {
		return err
	}
	if n < buf.Len() {
		err = io.ErrShortWrite
	}
	return err
}

func (me *Server) handleConnect(ctx context.Context, source RequestSourceAddr, tid udp.TransactionId) error {
	connId := randomConnectionId()
	err := me.ConnTracker.Add(ctx, source.String(), connId)
	if err != nil {
		err = fmt.Errorf("recording conn id: %w", err)
		return err
	}
	var buf bytes.Buffer
	udp.Write(&buf, udp.ResponseHeader{
		Action:        udp.ActionConnect,
		TransactionId: tid,
	})
	udp.Write(&buf, udp.ConnectionResponse{connId})
	n, err := me.SendResponse(ctx, buf.Bytes(), source)
	if err != nil {
		return err
	}
	if n < buf.Len() {
		err = io.ErrShortWrite
	}
	return err
}

func randomConnectionId() udp.ConnectionId {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic(err)
	}
	return binary.BigEndian.Uint64(b[:])
}

func RunSimple(ctx context.Context, s *Server, pc net.PacketConn, family udp.AddrFamily) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var b [1500]byte
	// Limit concurrent handled requests.
	sem := make(chan struct{}, 1000)
	for {
		n, addr, err := pc.ReadFrom(b[:])
		ctx, span := tracer.Start(ctx, "handle udp packet")
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return err
		}
		select {
		case <-ctx.Done():
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return ctx.Err()
		default:
			span.SetStatus(codes.Error, "concurrency limit reached")
			span.End()
			log.Levelf(log.Debug, "dropping request from %v: concurrency limit reached", addr)
			continue
		case sem <- struct{}{}:
		}
		b := append([]byte(nil), b[:n]...)
		go func() {
			defer span.End()
			defer func() { <-sem }()
			err := s.HandleRequest(ctx, family, addr, b)
			if err != nil {
				log.Printf("error handling %v byte request from %v: %v", n, addr, err)
			}
		}()
	}
}
