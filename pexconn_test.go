package torrent

import (
	"net"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/require"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func TestPexConnState(t *testing.T) {
	var cl Client
	cl.init(TestingConfig(t))
	cl.initLogger()
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
	require.True(t, c.pex.IsEnabled(), "should get enabled")
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
	require.True(t, writerCalled)
	require.EqualValues(t, pp.Extended, out.Type)
	require.NotEqualValues(t, out.ExtendedID, 0)
	require.EqualValues(t, c.PeerExtensionIDs[pp.ExtensionNamePex], out.ExtendedID)

	x, err := pp.LoadPexMsg(out.ExtendedPayload)
	require.NoError(t, err)
	targx := pp.PexMsg{
		Added:      krpc.CompactIPv4NodeAddrs(nil),
		AddedFlags: []pp.PexPeerFlags{},
		Added6: krpc.CompactIPv6NodeAddrs{
			krpcNodeAddrFromNetAddr(addr),
		},
		Added6Flags: []pp.PexPeerFlags{0},
	}
	require.EqualValues(t, targx, x)
}
