package torrent

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

const (
	pexRetryDelay = 10 * time.Second
	pexInterval   = 1 * time.Minute
	// Most added peers used from each address family of a received PEX message. BEP 11 asks
	// senders for no more than 50 per message.
	maxPexAddedPerMessage = 100
	// Most live connections recorded as purported by the remote end of a connection.
	maxPexRemoteLiveConns = 1000
)

// per-connection PEX state
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
	// Running record of live connections the remote end of the connection purports to have.
	remoteLiveConns map[netip.AddrPort]g.Option[pp.PexPeerFlags]
	lastRecv        time.Time
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

func (s *pexConnState) updateRemoteLiveConns(rx pp.PexMsg) (errs []error) {
	for _, dropped := range rx.Dropped {
		addrPort, _ := ipv4AddrPortFromKrpcNodeAddr(dropped)
		delete(s.remoteLiveConns, addrPort)
	}
	for _, dropped := range rx.Dropped6 {
		addrPort, _ := ipv6AddrPortFromKrpcNodeAddr(dropped)
		delete(s.remoteLiveConns, addrPort)
	}
	insert := func(addrPort netip.AddrPort, flags g.Option[pp.PexPeerFlags]) {
		if len(s.remoteLiveConns) >= maxPexRemoteLiveConns {
			if _, ok := s.remoteLiveConns[addrPort]; !ok {
				return
			}
		}
		g.MakeMapIfNil(&s.remoteLiveConns)
		g.MapInsert(s.remoteLiveConns, addrPort, flags)
	}
	for i, added := range rx.Added {
		addr := netip.AddrFrom4(*(*[4]byte)(added.IP.To4()))
		insert(netip.AddrPortFrom(addr, uint16(added.Port)), g.SliceGet(rx.AddedFlags, i))
	}
	for i, added := range rx.Added6 {
		addr := netip.AddrFrom16(*(*[16]byte)(added.IP.To16()))
		insert(netip.AddrPortFrom(addr, uint16(added.Port)), g.SliceGet(rx.Added6Flags, i))
	}
	return
}

// Limits the number of added peers taken from a message, and drops those the remote end may not
// direct us to dial. Flags are kept aligned with their peers.
func (s *pexConnState) filterAdded(
	nas []krpc.NodeAddr,
	fs []pp.PexPeerFlags,
) (retNas []krpc.NodeAddr, retFs []pp.PexPeerFlags) {
	for i, na := range nas {
		if len(retNas) >= maxPexAddedPerMessage {
			torrent.Add("pex added peers dropped past message limit", int64(len(nas)-i))
			break
		}
		addr, ok := netip.AddrFromSlice(na.IP)
		if !ok || !peerMayDirectDialTo(s.remoteIp, addr) {
			torrent.Add("pex added peers dropped for address scope", 1)
			continue
		}
		retNas = append(retNas, na)
		if i < len(fs) {
			retFs = append(retFs, fs[i])
		}
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
	rx.Added, rx.AddedFlags = s.filterAdded(rx.Added, rx.AddedFlags)
	rx.Added6, rx.Added6Flags = s.filterAdded(rx.Added6, rx.Added6Flags)
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
