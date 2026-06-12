package torrent

import (
	"net"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	qt "github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

func TestPexConnState(t *testing.T) {
	var cl Client
	cfg := TestingConfig(t)
	cfg.DefaultStorage = storage.NewFileWithCompletion(cfg.DataDir, storage.NewMapPieceCompletion())
	cl.init(cfg)
	t.Cleanup(func() {
		for _, f := range cl.onClose {
			f()
		}
	})
	torrent := cl.newTorrentForTesting()
	addr := &net.TCPAddr{IP: net.IPv6loopback, Port: 4747}
	c := cl.newConnection(nil, newConnectionOpts{
		remoteAddr: addr,
		network:    addr.Network(),
	})
	c.PeerExtensionIDs = make(map[pp.ExtensionName]pp.ExtensionNumber)
	c.PeerExtensionIDs[pp.ExtensionNamePex] = 1
	c.messageWriter.mu.Lock()
	c.setTorrent(torrent)
	if err := torrent.addPeerConn(c); err != nil {
		t.Log(err)
	}

	connWriteCond := c.messageWriter.writeCond.Signaled()
	c.pex.Init(c)
	qt.Assert(t, qt.IsTrue(c.pex.IsEnabled()), qt.Commentf("should get enabled"))
	defer c.pex.Close()

	var out pp.Message
	writerCalled := false
	testWriter := func(m pp.Message) bool {
		writerCalled = true
		out = m
		return true
	}
	<-connWriteCond
	c.pex.Share(testWriter)
	qt.Assert(t, qt.IsTrue(writerCalled))
	qt.Assert(t, qt.Equals(out.Type, pp.Extended))
	qt.Assert(t, qt.Not(qt.Equals(0, out.ExtendedID)))
	qt.Assert(t, qt.Equals(out.ExtendedID, c.PeerExtensionIDs[pp.ExtensionNamePex]))

	x, err := pp.LoadPexMsg(out.ExtendedPayload)
	qt.Assert(t, qt.IsNil(err))
	targx := pp.PexMsg{
		Added:      krpc.CompactIPv4NodeAddrs(nil),
		AddedFlags: []pp.PexPeerFlags{},
		Added6: krpc.CompactIPv6NodeAddrs{
			krpcNodeAddrFromNetAddr(addr),
		},
		Added6Flags: []pp.PexPeerFlags{0},
	}
	qt.Assert(t, qt.DeepEquals(x, targx))
}
