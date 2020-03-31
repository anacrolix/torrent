package torrent

import (
	"net"
	"testing"

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
		s.Add(&PeerConn{remoteAddr: addrs[0], outgoing: true})
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
		s.Add(&PeerConn{remoteAddr: addrs[0]})
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
		s.Add(&PeerConn{remoteAddr: addrs[0]})
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
		s.Drop(&PeerConn{remoteAddr: addrs[0]})
		targ := &pexState{
			hold: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc:   0,
		}
		require.EqualValues(t, targ, s)
	})
	t.Run("aboveTarg", func(t *testing.T) {
		s := &pexState{nc: pexTargAdded + 1}
		s.Drop(&PeerConn{remoteAddr: addrs[0]})
		targ := &pexState{
			ev: []pexEvent{pexEvent{pexDrop, addrs[0], 0}},
			nc: pexTargAdded,
		}
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
	targM *pp.PexMsg
	targS int
}{
	{
		name:  "empty",
		in:    &pexState{},
		arg:   0,
		targM: &pp.PexMsg{},
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
		targM: &pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				nodeAddr(addrs[2]),
				nodeAddr(addrs[3]),
			},
			AddedFlags: []pp.PexPeerFlags{f, f},
			Added6: krpc.CompactIPv6NodeAddrs{
				nodeAddr(addrs[0]),
				nodeAddr(addrs[1]),
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
		targM: &pp.PexMsg{
			Dropped: krpc.CompactIPv4NodeAddrs{
				nodeAddr(addrs[2]),
			},
			Dropped6: krpc.CompactIPv6NodeAddrs{
				nodeAddr(addrs[0]),
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
		targM: &pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				nodeAddr(addrs[1]),
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
		targM: &pp.PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				nodeAddr(addrs[2]),
			},
			AddedFlags: []pp.PexPeerFlags{f},
			Added6: krpc.CompactIPv6NodeAddrs{
				nodeAddr(addrs[0]),
				nodeAddr(addrs[1]),
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
		targM: &pp.PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				nodeAddr(addrs[1]),
			},
			Added6Flags: []pp.PexPeerFlags{f},
		},
		targS: 2,
	},
}

func TestPexGenmsg(t *testing.T) {
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.in
			m, seen := s.Genmsg(tc.arg)
			require.EqualValues(t, tc.targM, m)
			require.EqualValues(t, tc.targS, seen)
		})
	}
}
