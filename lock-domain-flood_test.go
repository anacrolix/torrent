package torrent

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Many conns on one Torrent, so one lock domain, flood it with messages: an extended handshake and
// a PEX message each, which are handled in exclusive mode, then Haves and interest changes, which
// are handled in domain turns. The domain's queues must stay bounded by the number of conns, since
// each read loop waits for its turn, and PEX mustn't add peers from every conn.
func TestLockDomainFloodStaysBounded(t *testing.T) {
	const (
		numConns     = 32
		numPieces    = 256
		peersPerPex  = 50
		interestFlip = 500
	)
	cfg := TestingConfig(t)
	cfg.DialForPeerConns = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	tor, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:                 testingTorrentInfoHash,
		Storage:                  &torrentStorageClient{ts: &torrentStorage{}},
		DisableInitialPieceCheck: true,
	})
	qt.Assert(t, qt.IsNil(tor.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, 20*numPieces),
		Length:      numPieces << 14,
		PieceLength: 1 << 14,
	})))
	extHandshake := pp.Message{
		Type:       pp.Extended,
		ExtendedID: pp.HandshakeExtendedID,
		ExtendedPayload: bencode.MustMarshal(pp.ExtendedHandshakeMessage{
			M: map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNamePex: 1},
		}),
	}.MustMarshalBinary()
	localPexId := cl.defaultLocalLtepProtocolMap.toSupportedExtensionDict()[pp.ExtensionNamePex]
	type floodConn struct {
		cn   *PeerConn
		w    net.Conn
		msgs []byte
	}
	conns := make([]floodConn, numConns)
	for i := range conns {
		r, w := net.Pipe()
		cn := cl.newConnection(r, newConnectionOpts{
			outgoing:   true,
			remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, byte(i >> 8), byte(i + 1)}), 1234),
			network:    r.RemoteAddr().Network(),
			connString: regularNetConnPeerConnConnString(r),
		})
		cn.PeerExtensionBytes = pp.NewPeerExtensionBytes(pp.ExtensionBitLtep)
		cn.LocalLtepProtocolMap = &cl.defaultLocalLtepProtocolMap
		cl.lock()
		cn.setTorrent(tor)
		qt.Assert(t, qt.IsNil(tor.addPeerConn(cn)))
		cn.startMessageWriter()
		cl.unlock()
		msgs := append([]byte(nil), extHandshake...)
		var pex pp.PexMsg
		for j := range peersPerPex {
			pex.Added = append(pex.Added, krpc.NodeAddr{IP: net.IPv4(2, 3, byte(i), byte(j+1)).To4(), Port: 6881})
			pex.AddedFlags = append(pex.AddedFlags, 0)
		}
		msgs = append(msgs, pex.Message(localPexId).MustMarshalBinary()...)
		for p := range numPieces {
			msgs = append(msgs, pp.Message{Type: pp.Have, Index: pp.Integer(p)}.MustMarshalBinary()...)
		}
		for j := range interestFlip {
			typ := pp.Interested
			if j%2 == 1 {
				typ = pp.NotInterested
			}
			msgs = append(msgs, pp.Message{Type: typ}.MustMarshalBinary()...)
		}
		conns[i] = floodConn{cn: cn, w: w, msgs: msgs}
		// Drain our replies so the writer never holds up the read loop.
		go io.Copy(io.Discard, w)
	}
	errs := make(chan error, numConns)
	for _, fc := range conns {
		go func() {
			// As in runHandshookConn.
			tor.lockDomain()
			err := fc.cn.mainReadLoop()
			fc.cn.close()
			tor.unlockDomain()
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				err = nil
			}
			errs <- err
		}()
	}
	writeErrs := make(chan error, numConns)
	for _, fc := range conns {
		go func() {
			_, err := fc.w.Write(fc.msgs)
			writeErrs <- err
		}()
	}
	for range conns {
		qt.Assert(t, qt.IsNil(<-writeErrs))
	}
	// Every message has been read, so all that's left is the last turn of each conn.
	for _, fc := range conns {
		fc.w.Close()
	}
	for range conns {
		qt.Check(t, qt.IsNil(<-errs))
	}
	stats := cl.Stats()
	t.Logf("turns queued max %v, exclusive actions max %v, pending peers %v",
		stats.LockDomainTurnsQueuedMax, stats.LockDomainExclusiveActionsMax, stats.PendingPeers)
	qt.Check(t, qt.IsTrue(stats.LockDomainTurnsQueuedMax <= numConns))
	qt.Check(t, qt.Equals(stats.LockDomainTurnsQueued, 0))
	// Exclusive actions are coalesced by key per Torrent, so they don't grow with messages.
	qt.Check(t, qt.IsTrue(stats.LockDomainExclusiveActionsMax <= 4))
	// After one PEX message adds peers, the Torrent ignores PEX peers for pexInterval.
	qt.Check(t, qt.IsTrue(stats.PendingPeers <= peersPerPex))
	// Every conn's extended handshake and PEX message was handled.
	tor.rLockDomain()
	for _, fc := range conns {
		qt.Check(t, qt.IsTrue(fc.cn.pex.Listed && fc.cn.pex.IsEnabled()))
	}
	tor.rUnlockDomain()
}
