package torrent

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/krpc"
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
	f = pp.PexOutgoingConn
)

func TestPexAdded(t *testing.T) {
	t.Run("noHold", func(t *testing.T) {
		s := new(pexState)
		s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[0], outgoing: true}})
		targ := &pexState{
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], pp.PexOutgoingConn},
			},
			nc: 1,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("belowTarg", func(t *testing.T) {
		s := &pexState{
			hold: []pexEvent{
				pexEvent{pexDrop, addrs[1], 0},
			},
			nc: 0,
		}
		s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}})
		targ := &pexState{
			hold: []pexEvent{
				pexEvent{pexDrop, addrs[1], 0},
			},
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], 0},
			},
			nc: 1,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTarg", func(t *testing.T) {
		holdAddr := &net.TCPAddr{IP: net.IPv6loopback, Port: 4848}
		s := &pexState{
			hold: []pexEvent{
				pexEvent{pexDrop, holdAddr, 0},
			},
			nc: pexTargAdded,
		}
		s.Add(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}})
		targ := &pexState{
			hold: []pexEvent{},
			ev: []pexEvent{
				pexEvent{pexDrop, holdAddr, 0},
				pexEvent{pexAdd, addrs[0], 0},
			},
			nc: pexTargAdded + 1,
		}
		require.EqualValues(t, targ, s)
	})
}

func TestPexDropped(t *testing.T) {
	t.Run("belowTarg", func(t *testing.T) {
		s := &pexState{nc: 1}
		s.Drop(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: true}})
		targ := &pexState{
			hold: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc:   0,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTarg", func(t *testing.T) {
		s := &pexState{nc: pexTargAdded + 1}
		s.Drop(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: true}})
		targ := &pexState{
			ev: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc: pexTargAdded,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTargNotListed", func(t *testing.T) {
		s := &pexState{nc: pexTargAdded + 1}
		s.Drop(&PeerConn{Peer: Peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: false}})
		targ := &pexState{nc: pexTargAdded + 1}
		require.EqualValues(t, targ, s)
	})
}

func TestPexReset(t *testing.T) {
	s := &pexState{
		hold: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
		ev:   []pexEvent{pexEvent{pexAdd, addrs[1], 0}},
		nc:   1,
	}
	s.Reset()
	targ := new(pexState)
	require.EqualValues(t, targ, s)
}

func mustNodeAddr(addr net.Addr) krpc.NodeAddr {
	ret, ok := nodeAddr(addr)
	if !ok {
		panic(addr)
	}
	return ret
}

var testcases = []struct {
	name  string
	in    *pexState
	arg   int
	targM pp.PexMsg
	targS int
}{
	{
		name:  "empty",
		in:    &pexState{},
		arg:   0,
		targM: pp.PexMsg{},
		targS: 0,
	},
	{
		name: "add4",
		in: &pexState{
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], f},
				pexEvent{pexAdd, addrs[1], f},
				pexEvent{pexAdd, addrs[2], f},
				pexEvent{pexAdd, addrs[3], f},
			},
		},
		arg: 0,
		targM: pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				mustNodeAddr(addrs[2]),
				mustNodeAddr(addrs[3]),
			},
			AddedFlags: []pp.PexPeerFlags{f, f},
			Added6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[0]),
				mustNodeAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{f, f},
		},
		targS: 4,
	},
	{
		name: "drop2",
		arg:  0,
		in: &pexState{
			ev: []pexEvent{
				pexEvent{pexDrop, addrs[0], f},
				pexEvent{pexDrop, addrs[2], f},
			},
		},
		targM: pp.PexMsg{
			Dropped: krpc.CompactIPv4NodeAddrs{
				mustNodeAddr(addrs[2]),
			},
			Dropped6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[0]),
			},
		},
		targS: 2,
	},
	{
		name: "add2drop1",
		arg:  0,
		in: &pexState{
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], f},
				pexEvent{pexAdd, addrs[1], f},
				pexEvent{pexDrop, addrs[0], f},
			},
		},
		targM: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{f},
		},
		targS: 3,
	},
	{
		name: "delayed",
		arg:  0,
		in: &pexState{
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], f},
				pexEvent{pexAdd, addrs[1], f},
				pexEvent{pexAdd, addrs[2], f},
			},
			hold: []pexEvent{
				pexEvent{pexDrop, addrs[0], f},
				pexEvent{pexDrop, addrs[2], f},
				pexEvent{pexDrop, addrs[1], f},
			},
		},
		targM: pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				mustNodeAddr(addrs[2]),
			},
			AddedFlags: []pp.PexPeerFlags{f},
			Added6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[0]),
				mustNodeAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{f, f},
		},
		targS: 3,
	},
	{
		name: "followup",
		arg:  1,
		in: &pexState{
			ev: []pexEvent{
				pexEvent{pexAdd, addrs[0], f},
				pexEvent{pexAdd, addrs[1], f},
			},
		},
		targM: pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{f},
		},
		targS: 2,
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

func TestPexGenmsg(t *testing.T) {
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.in
			m, seen := s.Genmsg(tc.arg)
			assertPexMsgsEqual(t, tc.targM, m)
			require.EqualValues(t, tc.targS, seen)
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
	m, seq := s.Genmsg(0)

	require.EqualValues(t, n, seq)
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
			s.Genmsg(0)
		}
	}
}

// obtain at least 5 points, e.g. to plot a graph
func BenchmarkPexInitial4(b *testing.B)   { benchmarkPexInitialN(b, 4) }
func BenchmarkPexInitial50(b *testing.B)  { benchmarkPexInitialN(b, 50) }
func BenchmarkPexInitial100(b *testing.B) { benchmarkPexInitialN(b, 100) }
func BenchmarkPexInitial200(b *testing.B) { benchmarkPexInitialN(b, 200) }
func BenchmarkPexInitial400(b *testing.B) { benchmarkPexInitialN(b, 400) }

func TestPexAdd(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		var m pexMsgFactory
		m.addEvent(pexEvent{pexDrop, addrs[0], 0})
		m.addEvent(pexEvent{pexAdd, addrs[1], f})
		for _, addr := range addrs {
			m.addEvent(pexEvent{pexAdd, addr, f})
		}
		targ := pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				mustNodeAddr(addrs[1]),
				mustNodeAddr(addrs[2]),
				mustNodeAddr(addrs[3]),
			},
			AddedFlags: []pp.PexPeerFlags{f, f, f},
		}
		out := m.PexMsg()
		assertPexMsgsEqual(t, targ, out)
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		var m pexMsgFactory
		m.addEvent(pexEvent{pexDrop, addrs[0], 0})
		m.addEvent(pexEvent{pexAdd, addrs[1], f})
		for _, addr := range addrs {
			m.addEvent(pexEvent{pexAdd, addr, f})
		}
		targ := pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[1]),
				mustNodeAddr(addrs[2]),
				mustNodeAddr(addrs[3]),
			},
			Added6Flags: []pp.PexPeerFlags{f, f, f},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("empty", func(t *testing.T) {
		nullAddr := &net.TCPAddr{}
		var xm pexMsgFactory
		xm.addEvent(pexEvent{pexAdd, nullAddr, f})
		m := xm.PexMsg()
		require.EqualValues(t, 0, len(m.Added))
		require.EqualValues(t, 0, len(m.AddedFlags))
		require.EqualValues(t, 0, len(m.Added6))
		require.EqualValues(t, 0, len(m.Added6Flags))
	})
}

func TestPexDrop(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		var m pexMsgFactory
		m.addEvent(pexEvent{pexAdd, addrs[0], f})
		m.addEvent(pexEvent{pexDrop, addrs[1], 0})
		for _, addr := range addrs {
			m.addEvent(pexEvent{pexDrop, addr, 0})
		}
		targ := pp.PexMsg{
			Dropped: krpc.CompactIPv4NodeAddrs{
				mustNodeAddr(addrs[1]),
				mustNodeAddr(addrs[2]),
				mustNodeAddr(addrs[3]),
			},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		var m pexMsgFactory
		m.addEvent(pexEvent{pexAdd, addrs[0], f})
		m.addEvent(pexEvent{pexDrop, addrs[1], 0})
		for _, addr := range addrs {
			m.addEvent(pexEvent{pexDrop, addr, 0})
		}
		targ := pp.PexMsg{
			Dropped6: krpc.CompactIPv6NodeAddrs{
				mustNodeAddr(addrs[1]),
				mustNodeAddr(addrs[2]),
				mustNodeAddr(addrs[3]),
			},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("empty", func(t *testing.T) {
		nullAddr := &net.TCPAddr{}
		var xm pexMsgFactory
		xm.addEvent(pexEvent{pexDrop, nullAddr, f})
		m := xm.PexMsg()
		require.EqualValues(t, 0, len(m.Dropped))
		require.EqualValues(t, 0, len(m.Dropped6))
	})
}
