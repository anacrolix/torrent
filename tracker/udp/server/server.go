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

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/tracker/udp"
)

type ConnectionTrackerAddr = string

type ConnectionTracker interface {
	Add(ctx context.Context, addr ConnectionTrackerAddr, id udp.ConnectionId) error
	Check(ctx context.Context, addr ConnectionTrackerAddr, id udp.ConnectionId) (bool, error)
}

type InfoHash = [20]byte

type AnnounceTracker = tracker.AnnounceTracker

type Server struct {
	ConnTracker  ConnectionTracker
	SendResponse func(data []byte, addr net.Addr) (int, error)
	Announce     tracker.AnnounceHandler
}

type RequestSourceAddr = net.Addr

func (me *Server) HandleRequest(
	ctx context.Context,
	family udp.AddrFamily,
	source RequestSourceAddr,
	body []byte,
) error {
	var h udp.RequestHeader
	var r bytes.Reader
	r.Reset(body)
	err := udp.Read(&r, &h)
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
	opts := tracker.GetPeersOpts{MaxCount: generics.Some[uint](50)}
	if addrFamily == udp.AddrFamilyIpv4 {
		opts.MaxCount = generics.Some[uint](150)
	}
	peers, err := me.Announce.Serve(ctx, req, announceAddr, opts)
	if err != nil {
		return err
	}
	nodeAddrs := make([]krpc.NodeAddr, 0, len(peers))
	for _, p := range peers {
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
	err = udp.Write(&buf, udp.AnnounceResponseHeader{})
	if err != nil {
		return err
	}
	b, err := udp.GetNodeAddrsCompactMarshaler(nodeAddrs, addrFamily).MarshalBinary()
	if err != nil {
		err = fmt.Errorf("marshalling compact node addrs: %w", err)
		return err
	}
	buf.Write(b)
	n, err := me.SendResponse(buf.Bytes(), source)
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
	n, err := me.SendResponse(buf.Bytes(), source)
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
	for {
		var b [1500]byte
		n, addr, err := pc.ReadFrom(b[:])
		if err != nil {
			return err
		}
		go func() {
			err := s.HandleRequest(ctx, family, addr, b[:n])
			if err != nil {
				log.Printf("error handling %v byte request from %v: %v", n, addr, err)
			}
		}()
	}
}
