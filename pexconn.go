package torrent

import (
	"fmt"
	"net/netip"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	pexRetryDelay = 10 * time.Second
	pexInterval   = 1 * time.Minute
)

// per-connection PEX state
type pexConnState struct {
	enabled bool
	xid     pp.ExtensionNumber
	last    *pexEvent
	timer   *time.Timer
	gate    chan struct{}
	readyfn func()
	torrent *Torrent
	Listed  bool
	logger  log.Logger
	// Running record of live connections the remote end of the connection purports to have.
	remoteLiveConns map[netip.AddrPort]g.Option[pp.PexPeerFlags]
	lastRecv        time.Time
}

func (s *pexConnState) IsEnabled() bool {
	return s.enabled
}

// Init is called from the reader goroutine upon the extended handshake completion
func (s *pexConnState) Init(c *PeerConn) {
	xid, ok := c.PeerExtensionIDs[pp.ExtensionNamePex]
	if !ok || xid == 0 || c.t.cl.config.DisablePEX {
		return
	}
	s.xid = xid
	s.last = nil
	s.torrent = c.t
	s.logger = c.logger.WithDefaultLevel(log.Debug).WithNames("pex")
	s.readyfn = c.tickleWriter
	s.gate = make(chan struct{}, 1)
	s.timer = time.AfterFunc(0, func() {
		s.gate <- struct{}{}
		s.readyfn() // wake up the writer
	})
	s.enabled = true
}

// schedule next PEX message
func (s *pexConnState) sched(delay time.Duration) {
	s.timer.Reset(delay)
}

// generate next PEX message for the peer; returns nil if nothing yet to send
func (s *pexConnState) genmsg() *pp.PexMsg {
	tx, last := s.torrent.pex.Genmsg(s.last)
	if tx.Len() == 0 {
		return nil
	}
	s.last = last
	return &tx
}

func (s *pexConnState) numPending() int {
	if s.torrent == nil {
		return 0
	}
	return s.torrent.pex.numPending(s.last)
}

// Share is called from the writer goroutine if when it is woken up with the write buffers empty
// Returns whether there's more room on the send buffer to write to.
func (s *pexConnState) Share(postfn messageWriter) bool {
	select {
	case <-s.gate:
		if tx := s.genmsg(); tx != nil {
			s.logger.Print("sending PEX message: ", tx)
			flow := postfn(tx.Message(s.xid))
			s.sched(pexInterval)
			return flow
		} else {
			// no PEX to send this time - try again shortly
			s.sched(pexRetryDelay)
		}
	default:
	}
	return true
}

func (s *pexConnState) updateRemoteLiveConns(rx pp.PexMsg) (errs []error) {
	for _, dropped := range rx.Dropped {
		addrPort, _ := ipv4AddrPortFromKrpcNodeAddr(dropped)
		delete(s.remoteLiveConns, addrPort)
	}
	for _, dropped := range rx.Dropped6 {
		addrPort, _ := ipv6AddrPortFromKrpcNodeAddr(dropped)
		delete(s.remoteLiveConns, addrPort)
	}
	for i, added := range rx.Added {
		addr := netip.AddrFrom4(*(*[4]byte)(added.IP.To4()))
		addrPort := netip.AddrPortFrom(addr, uint16(added.Port))
		flags := g.SliceGet(rx.AddedFlags, i)
		g.MakeMapIfNilAndSet(&s.remoteLiveConns, addrPort, flags)
	}
	for i, added := range rx.Added6 {
		addr := netip.AddrFrom16(*(*[16]byte)(added.IP.To16()))
		addrPort := netip.AddrPortFrom(addr, uint16(added.Port))
		flags := g.SliceGet(rx.Added6Flags, i)
		g.MakeMapIfNilAndSet(&s.remoteLiveConns, addrPort, flags)
	}
	return
}

// Recv is called from the reader goroutine
func (s *pexConnState) Recv(payload []byte) error {
	rx, err := pp.LoadPexMsg(payload)
	if err != nil {
		return fmt.Errorf("unmarshalling pex message: %w", err)
	}
	s.logger.Printf("received pex message: %v", rx)
	torrent.Add("pex added peers received", int64(len(rx.Added)))
	torrent.Add("pex added6 peers received", int64(len(rx.Added6)))

	// "Clients must batch updates to send no more than 1 PEX message per minute."
	timeSinceLastRecv := time.Since(s.lastRecv)
	if timeSinceLastRecv < 45*time.Second {
		return fmt.Errorf("last received only %v ago", timeSinceLastRecv)
	}
	s.lastRecv = time.Now()
	s.updateRemoteLiveConns(rx)

	var peers peerInfos
	peers.AppendFromPex(rx.Added6, rx.Added6Flags)
	peers.AppendFromPex(rx.Added, rx.AddedFlags)
	if time.Now().Before(s.torrent.pex.rest) {
		s.logger.Printf("in cooldown period, incoming PEX discarded")
		return nil
	}
	added := s.torrent.addPeers(peers)
	s.logger.Printf("got %v peers over pex, added %v", len(peers), added)

	if len(peers) > 0 {
		s.torrent.pex.rest = time.Now().Add(pexInterval)
	}

	// one day we may also want to:
	// - handle drops somehow
	// - detect malicious peers

	return nil
}

func (s *pexConnState) Close() {
	if s.timer != nil {
		s.timer.Stop()
	}
}
