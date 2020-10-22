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
	addrs = []net.Addr{
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4747},
		&net.TCPAddr{IP: net.IPv6loopback, Port: 4748},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747},
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4748},
	}
	f = pp.PexOutgoingConn
)

func TestPexAdded(t *testing.T) {
	t.Run("noHold", func(t *testing.T) {
		s := new(pexState)
		s.Add(&PeerConn{peer: peer{RemoteAddr: addrs[0], outgoing: true}})
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
		s.Add(&PeerConn{peer: peer{RemoteAddr: addrs[0]}})
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
		s.Add(&PeerConn{peer: peer{RemoteAddr: addrs[0]}})
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
		s.Drop(&PeerConn{peer: peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: true}})
		targ := &pexState{
			hold: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc:   0,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTarg", func(t *testing.T) {
		s := &pexState{nc: pexTargAdded + 1}
		s.Drop(&PeerConn{peer: peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: true}})
		targ := &pexState{
			ev: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc: pexTargAdded,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTargNotListed", func(t *testing.T) {
		s := &pexState{nc: pexTargAdded + 1}
		s.Drop(&PeerConn{peer: peer{RemoteAddr: addrs[0]}, pex: pexConnState{Listed: false}})
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
	added, added6     []pexMsgAdded
	dropped, dropped6 []krpc.NodeAddr
}

func (me *comparablePexMsg) makeAdded(addrs []krpc.NodeAddr, flags []pp.PexPeerFlags) (ret []pexMsgAdded) {
	for i, addr := range addrs {
		ret = append(ret, pexMsgAdded{
			NodeAddr:     addr,
			PexPeerFlags: flags[i],
		})
	}
	return
}

// Such Rust-inspired.
func (me *comparablePexMsg) From(f pp.PexMsg) {
	me.added = me.makeAdded(f.Added, f.AddedFlags)
	me.added6 = me.makeAdded(f.Added6, f.Added6Flags)
	me.dropped = f.Dropped
	me.dropped6 = f.Dropped6
}

// For PexMsg created by pexMsgFactory, this is as good as it can get without using data structures
// in pexMsgFactory that preserve insert ordering.
func (actual comparablePexMsg) AssertEqual(t *testing.T, expected comparablePexMsg) {
	assert.ElementsMatch(t, expected.added, actual.added)
	assert.ElementsMatch(t, expected.added6, actual.added6)
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

func TestPexAdd(t *testing.T) {
	addrs4 := []krpc.NodeAddr{
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747}, // 0
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4748}, // 1
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 2), Port: 4747}, // 2
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 2), Port: 4748}, // 3
	}
	addrs6 := []krpc.NodeAddr{
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4747}, // 0
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4748}, // 1
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4749}, // 2
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4750}, // 3
	}
	f := pp.PexPrefersEncryption | pp.PexOutgoingConn

	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		var m pexMsgFactory
		m.Drop(addrs[0])
		m.Add(addrs[1], f)
		for _, addr := range addrs {
			m.Add(addr, f)
		}
		targ := pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
			AddedFlags: []pp.PexPeerFlags{f, f, f},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		var m pexMsgFactory
		m.Drop(addrs[0])
		m.Add(addrs[1], f)
		for _, addr := range addrs {
			m.Add(addr, f)
		}
		targ := pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
			Added6Flags: []pp.PexPeerFlags{f, f, f},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("empty", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		var xm pexMsgFactory
		assert.Panics(t, func() { xm.Add(addr, f) })
		m := xm.PexMsg()
		require.EqualValues(t, 0, len(m.Added))
		require.EqualValues(t, 0, len(m.AddedFlags))
		require.EqualValues(t, 0, len(m.Added6))
		require.EqualValues(t, 0, len(m.Added6Flags))
	})
}

func TestPexDrop(t *testing.T) {
	addrs4 := []krpc.NodeAddr{
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747}, // 0
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4748}, // 1
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 2), Port: 4747}, // 2
		krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 2), Port: 4748}, // 3
	}
	addrs6 := []krpc.NodeAddr{
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4747}, // 0
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4748}, // 1
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4749}, // 2
		krpc.NodeAddr{IP: net.IPv6loopback, Port: 4750}, // 3
	}
	f := pp.PexPrefersEncryption | pp.PexOutgoingConn

	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		var m pexMsgFactory
		m.Add(addrs[0], f)
		m.Drop(addrs[1])
		for _, addr := range addrs {
			m.Drop(addr)
		}
		targ := pp.PexMsg{
			Dropped: krpc.CompactIPv4NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		var m pexMsgFactory
		m.Add(addrs[0], f)
		m.Drop(addrs[1])
		for _, addr := range addrs {
			m.Drop(addr)
		}
		targ := pp.PexMsg{
			Dropped6: krpc.CompactIPv6NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
		}
		assertPexMsgsEqual(t, targ, m.PexMsg())
	})
	t.Run("empty", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		var xm pexMsgFactory
		require.Panics(t, func() { xm.Drop(addr) })
		m := xm.PexMsg()
		require.EqualValues(t, 0, len(m.Dropped))
		require.EqualValues(t, 0, len(m.Dropped6))
	})
}
