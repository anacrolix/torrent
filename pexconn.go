package torrent

import (
	"fmt"
	"net/netip"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"

	peerWire "github.com/anacrolix/torrent/internal/peer-wire"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	pexRetryDelay = 10 * time.Second
	pexInterval   = 1 * time.Minute
)

// Per-connection PEX state for sending, and for acting on what the peer sends. What the peer has
// told us is in peerWire.State.
type pexConnState struct {
	enabled bool
	xid     pp.ExtensionNumber
	last    *pexEvent
	timer   *releasedTimer
	gate    chan struct{}
	readyfn func()
	torrent *Torrent
	Listed  bool
	logger  log.Logger
	// The remote end's IP, if known.
	remoteIp g.Option[netip.Addr]
}

func (s *pexConnState) IsEnabled() bool {
	return s.enabled
}

// Init is called from the reader goroutine upon the extended handshake completion
func (s *pexConnState) Init(c *PeerConn) {
	if s.enabled {
		// The extended handshake can be repeated. Keep the existing state and timer.
		return
	}
	xid, ok := c.PeerExtensionIDs[pp.ExtensionNamePex]
	if !ok || xid == 0 || c.t.cl.config.DisablePEX {
		return
	}
	// BEP 27: private torrents must not exchange peers via PEX.
	if c.t.isPrivate() {
		return
	}
	s.xid = xid
	s.last = nil
	s.torrent = c.t
	s.remoteIp = c.bannableAddr
	s.logger = c.logger.WithDefaultLevel(log.Debug).WithNames("pex")
	s.readyfn = c.tickleWriter
	s.gate = make(chan struct{}, 1)
	s.timer = newReleasedTimer(0, func() {
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

// Recv is called from the reader goroutine
func (s *pexConnState) Recv(w *peerWire.State, payload []byte) error {
	rx, err := pp.LoadPexMsg(payload)
	if err != nil {
		return fmt.Errorf("unmarshalling pex message: %w", err)
	}
	s.logger.Printf("received pex message: %v", rx)
	torrent.Add("pex added peers received", int64(len(rx.Added)))
	torrent.Add("pex added6 peers received", int64(len(rx.Added6)))

	dropped, err := w.RecvPex(&rx, time.Now(), func(addr netip.Addr) bool {
		return peerMayDirectDialTo(s.remoteIp, addr)
	})
	if err != nil {
		return err
	}
	torrent.Add("pex added peers dropped past message limit", int64(dropped.DroppedPastLimit))
	torrent.Add("pex added peers dropped for address scope", int64(dropped.DroppedForScope))

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

// Stops PEX on the conn, for when we learn the torrent is private after PEX was enabled.
func (s *pexConnState) disable() {
	s.Close()
	s.enabled = false
}

func (s *pexConnState) Close() {
	if s.timer != nil {
		s.timer.Stop()
	}
}
