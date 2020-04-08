package torrent

import (
	"fmt"
	"time"

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
	seq     int
	timer   *time.Timer
	gate    chan struct{}
	readyfn func()
	torrent *Torrent
	Listed  bool
	info    log.Logger
	dbg     log.Logger
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
	s.seq = 0
	s.torrent = c.t
	s.info = c.t.cl.logger
	s.dbg = c.logger
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
	tx, seq := s.torrent.pex.Genmsg(s.seq)
	if tx.Len() == 0 {
		return nil
	}
	s.seq = seq
	return tx
}

// Share is called from the writer goroutine if when it is woken up with the write buffers empty
func (s *pexConnState) Share(postfn messageWriter) {
	select {
	case <-s.gate:
		if tx := s.genmsg(); tx != nil {
			s.dbg.Print("sending PEX message: ", tx)
			postfn(tx.Message(s.xid))
			s.sched(pexInterval)
		} else {
			// no PEX to send this time - try again shortly
			s.sched(pexRetryDelay)
		}
	default:
		return
	}
}

// Recv is called from the reader goroutine
func (s *pexConnState) Recv(payload []byte) error {
	rx, err := pp.LoadPexMsg(payload)
	if err != nil {
		return fmt.Errorf("error unmarshalling PEX message: %s", err)
	}
	s.dbg.Print("incoming PEX message: ", rx)
	torrent.Add("pex added peers received", int64(len(rx.Added)))
	torrent.Add("pex added6 peers received", int64(len(rx.Added6)))

	var peers Peers
	peers.AppendFromPex(rx.Added6, rx.Added6Flags)
	peers.AppendFromPex(rx.Added, rx.AddedFlags)
	s.dbg.Printf("adding %d peers from PEX", len(peers))
	s.torrent.addPeers(peers)
	// s.dbg.Print("known swarm now:", s.torrent.KnownSwarm())

	// one day we may also want to:
	// - check if the peer is not flooding us with PEX updates
	// - handle drops somehow
	// - detect malicious peers

	return nil
}

func (s *pexConnState) Close() {
	if s.timer != nil {
		s.timer.Stop()
	}
}
