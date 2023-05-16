package torrent

import (
	"net"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

var (
	addrs6 = []net.Addr{
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4747},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4748},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4749},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4750},
	}
	addrs4 = []net.Addr{
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4748},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4749},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4750},
	}
	addrs = []net.Addr{
		addrs6[0],
		addrs6[1],
		addrs4[0],
		addrs4[1],
	}
)

func TestPexReset(t *testing.T) {
	s := &pexState{}
	conns := []PeerConn{
		{Peer: Peer{RemoteAddr: addrs[0]}},
		{Peer: Peer{RemoteAddr: addrs[1]}},
		{Peer: Peer{RemoteAddr: addrs[2]}},
	}
	s.Add(&conns[0])
	s.Add(&conns[1])
	s.Drop(&conns[0])
	s.Reset()
	targ := new(pexState)
	require.EqualValues(t, targ, s)
}

func krpcNodeAddrFromNetAddr(addr net.Addr) krpc.NodeAddr {
	addrPort, err := addrPortFromPeerRemoteAddr(addr)
	if err != nil {
		panic(err)
	}
	return krpcNodeAddrFromAddrPort(addrPort)
}

var testcases = []struct {
	name   string
	in     *pexState
	targ   pp.PexMsg
	update func(*pexState)
	targ1  pp.PexMsg
}{
	{
		name: "empty",
		in:   &pexState{},
		targ: pp.PexMsg{},
	},
	{
		name: "add0",
		in: func() *pexState {
			s := new(pexState)
			nullAddr := &net.TCPAddr{}
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: nullAddr}})
			return s
		}(),
		targ: pp.PexMsg{},
	},
	{
		name: "drop0",
		in: func() *pexState {
			nullAddr := &net.TCPAddr{}
			s := new(pexState)
			s.Drop(&PeerConn{Peer: Peer{RemoteAddr: nullAddr}, pex: pexConnState{Listed: true}})
			return s
		}(),
		targ: pp.PexMsg{},
	},
	{
		name: "add4",
		in: func() *pexState {
			s := new(pexState)
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}})
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[1], outgoing: true}})
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[2], outgoing: true}})
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[3]}})
			return s
		}(),
		targ: pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[2]),
				krpcNodeAddrFromNetAddr(addrs[3]),
			},
			AddedFlags: []pp.PexPeerFlags{pp.PexOutgoingConn, 0},
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[0]),
				krpcNodeAddrFromNetAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{0, pp.PexOutgoingConn},
		},
	},
	{
		name: "drop2",
		in: func() *pexState {
			s := &pexState{nc: pexTargAdded + 2}
			s.Drop(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: true}})
			s.Drop(&PeerConn{Peer: Peer{RemoteAddr: addrs[2]}, pex: pexConnState{Listed: true}})
			return s
		}(),
		targ: pp.PexMsg{
			Dropped: krpc.CompactIPv4NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[2]),
			},
			Dropped6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[0]),
			},
		},
	},
	{
		name: "add2drop1",
		in: func() *pexState {
			conns := []PeerConn{
				{Peer: Peer{RemoteAddr: addrs[0]}},
				{Peer: Peer{RemoteAddr: addrs[1]}},
				{Peer: Peer{RemoteAddr: addrs[2]}},
			}
			s := &pexState{nc: pexTargAdded}
			s.Add(&conns[0])
			s.Add(&conns[1])
			s.Drop(&conns[0])
			s.Drop(&conns[2]) // to be ignored: it wasn't added
			return s
		}(),
		targ: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{0},
		},
	},
	{
		name: "delayed",
		in: func() *pexState {
			conns := []PeerConn{
				{Peer: Peer{RemoteAddr: addrs[0]}},
				{Peer: Peer{RemoteAddr: addrs[1]}},
				{Peer: Peer{RemoteAddr: addrs[2]}},
			}
			s := new(pexState)
			s.Add(&conns[0])
			s.Add(&conns[1])
			s.Add(&conns[2])
			s.Drop(&conns[0]) // on hold: s.nc < pexTargAdded
			s.Drop(&conns[2])
			s.Drop(&conns[1])
			return s
		}(),
		targ: pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[2]),
			},
			AddedFlags: []pp.PexPeerFlags{0},
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[0]),
				krpcNodeAddrFromNetAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{0, 0},
		},
	},
	{
		name: "unheld",
		in: func() *pexState {
			conns := []PeerConn{
				{Peer: Peer{RemoteAddr: addrs[0]}},
				{Peer: Peer{RemoteAddr: addrs[1]}},
			}
			s := &pexState{nc: pexTargAdded - 1}
			s.Add(&conns[0])
			s.Drop(&conns[0]) // on hold: s.nc < pexTargAdded
			s.Add(&conns[1])  // unholds the above
			return s
		}(),
		targ: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{0},
		},
	},
	{
		name: "followup",
		in: func() *pexState {
			s := new(pexState)
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}})
			return s
		}(),
		targ: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[0]),
			},
			Added6Flags: []pp.PexPeerFlags{0},
		},
		update: func(s *pexState) {
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[1]}})
		},
		targ1: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				krpcNodeAddrFromNetAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{0},
		},
	},
}

// Represents the contents of a PexMsg in a way that supports equivalence checking in tests. This is
// necessary because pexMsgFactory uses maps and so ordering of the resultant PexMsg isn't
// deterministic. Because the flags are in a different array, we can't just use testify's
// ElementsMatch because the ordering *does* still matter between an added addr and its flags.
type comparablePexMsg struct {
	added, added6           []krpc.NodeAddr
	addedFlags, added6Flags []pp.PexPeerFlags
	dropped, dropped6       []krpc.NodeAddr
}

// Such Rust-inspired.
func (me *comparablePexMsg) From(f pp.PexMsg) {
	me.added = f.Added
	me.addedFlags = f.AddedFlags
	me.added6 = f.Added6
	me.added6Flags = f.Added6Flags
	me.dropped = f.Dropped
	me.dropped6 = f.Dropped6
}

// For PexMsg created by pexMsgFactory, this is as good as it can get without using data structures
// in pexMsgFactory that preserve insert ordering.
func (actual comparablePexMsg) AssertEqual(t *testing.T, expected comparablePexMsg) {
	assert.ElementsMatch(t, expected.added, actual.added)
	assert.ElementsMatch(t, expected.addedFlags, actual.addedFlags)
	assert.ElementsMatch(t, expected.added6, actual.added6)
	assert.ElementsMatch(t, expected.added6Flags, actual.added6Flags)
	assert.ElementsMatch(t, expected.dropped, actual.dropped)
	assert.ElementsMatch(t, expected.dropped6, actual.dropped6)
}

func assertPexMsgsEqual(t *testing.T, expected, actual pp.PexMsg) {
	var ec, ac comparablePexMsg
	ec.From(expected)
	ac.From(actual)
	ac.AssertEqual(t, ec)
}

func TestPexGenmsg0(t *testing.T) {
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := *tc.in
			m, last := s.Genmsg(nil)
			assertPexMsgsEqual(t, tc.targ, m)
			if tc.update != nil {
				tc.update(&s)
				m1, last := s.Genmsg(last)
				assertPexMsgsEqual(t, tc.targ1, m1)
				assert.NotNil(t, last)
			}
		})
	}
}

// generate 𝑛 distinct values of net.Addr
func addrgen(n int) chan net.Addr {
	c := make(chan net.Addr)
	go func() {
		defer close(c)
		for i := 4747; i < 65535 && n > 0; i++ {
			c <- &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: i}
			n--
		}
	}()
	return c
}

func TestPexInitialNoCutoff(t *testing.T) {
	const n = 2 * pexMaxDelta
	var s pexState

	c := addrgen(n)
	for addr := range c {
		s.Add(&PeerConn{Peer: Peer{RemoteAddr: addr}})
	}
	m, _ := s.Genmsg(nil)

	require.EqualValues(t, n, len(m.Added))
	require.EqualValues(t, n, len(m.AddedFlags))
	require.EqualValues(t, 0, len(m.Added6))
	require.EqualValues(t, 0, len(m.Added6Flags))
	require.EqualValues(t, 0, len(m.Dropped))
	require.EqualValues(t, 0, len(m.Dropped6))
}

func benchmarkPexInitialN(b *testing.B, npeers int) {
	for i := 0; i < b.N; i++ {
		var s pexState
		c := addrgen(npeers)
		for addr := range c {
			s.Add(&PeerConn{Peer: Peer{RemoteAddr: addr}})
			s.Genmsg(nil)
		}
	}
}

// obtain at least 5 points, e.g. to plot a graph
func BenchmarkPexInitial4(b *testing.B)   { benchmarkPexInitialN(b, 4) }
func BenchmarkPexInitial50(b *testing.B)  { benchmarkPexInitialN(b, 50) }
func BenchmarkPexInitial100(b *testing.B) { benchmarkPexInitialN(b, 100) }
func BenchmarkPexInitial200(b *testing.B) { benchmarkPexInitialN(b, 200) }
func BenchmarkPexInitial400(b *testing.B) { benchmarkPexInitialN(b, 400) }
