package peer_protocol

import (
	"bufio"
	"bytes"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/bencode"
)

func TestUnmarshalPex(t *testing.T) {
	var pem PexMsg
	err := bencode.Unmarshal([]byte("d5:added12:\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0ce"), &pem)
	require.NoError(t, err)
	require.EqualValues(t, 2, len(pem.Added))
	require.EqualValues(t, 1286, pem.Added[0].Port)
	require.EqualValues(t, 0x100*0xb+0xc, pem.Added[1].Port)
}

func TestEmptyPexMsg(t *testing.T) {
	pm := PexMsg{}
	b, err := bencode.Marshal(pm)
	t.Logf("%q", b)
	require.NoError(t, err)
	require.NoError(t, bencode.Unmarshal(b, &pm))
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
	f := PexPrefersEncryption | PexOutgoingConn

	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		m := new(PexMsg)
		m.Drop(addrs[0])
		m.Add(addrs[1], f)
		for _, addr := range addrs {
			m.Add(addr, f)
		}
		targ := &PexMsg{
			Added: krpc.CompactIPv4NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
			AddedFlags: []PexPeerFlags{f, f, f},
			Dropped:    krpc.CompactIPv4NodeAddrs{},
		}
		require.EqualValues(t, targ, m)
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		m := new(PexMsg)
		m.Drop(addrs[0])
		m.Add(addrs[1], f)
		for _, addr := range addrs {
			m.Add(addr, f)
		}
		targ := &PexMsg{
			Added6: krpc.CompactIPv6NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
			Added6Flags: []PexPeerFlags{f, f, f},
			Dropped6:    krpc.CompactIPv6NodeAddrs{},
		}
		require.EqualValues(t, targ, m)
	})
	t.Run("empty", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		xm := new(PexMsg)
		xm.Add(addr, f)
		require.EqualValues(t, 0, len(xm.Added))
		require.EqualValues(t, 0, len(xm.AddedFlags))
		require.EqualValues(t, 0, len(xm.Added6))
		require.EqualValues(t, 0, len(xm.Added6Flags))
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
	f := PexPrefersEncryption | PexOutgoingConn

	t.Run("ipv4", func(t *testing.T) {
		addrs := addrs4
		m := new(PexMsg)
		m.Add(addrs[0], f)
		m.Drop(addrs[1])
		for _, addr := range addrs {
			m.Drop(addr)
		}
		targ := &PexMsg{
			AddedFlags: []PexPeerFlags{},
			Added:    krpc.CompactIPv4NodeAddrs{},
			Dropped: krpc.CompactIPv4NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
		}
		require.EqualValues(t, targ, m)
	})
	t.Run("ipv6", func(t *testing.T) {
		addrs := addrs6
		m := new(PexMsg)
		m.Add(addrs[0], f)
		m.Drop(addrs[1])
		for _, addr := range addrs {
			m.Drop(addr)
		}
		targ := &PexMsg{
			Added6Flags: []PexPeerFlags{},
			Added6:    krpc.CompactIPv6NodeAddrs{},
			Dropped6: krpc.CompactIPv6NodeAddrs{
				addrs[1],
				addrs[2],
				addrs[3],
			},
		}
		require.EqualValues(t, targ, m)
	})
	t.Run("empty", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		xm := new(PexMsg)
		xm.Drop(addr)
		require.EqualValues(t, 0, len(xm.Dropped))
		require.EqualValues(t, 0, len(xm.Dropped6))
	})
}

func TestMarshalPexMessage(t *testing.T) {
	addr := krpc.NodeAddr{IP: net.IP{127, 0, 0, 1}, Port: 0x55aa}
	f := PexPrefersEncryption | PexOutgoingConn
	pm := new(PexMsg)
	pm.Added = append(pm.Added, addr)
	pm.AddedFlags = append(pm.AddedFlags, f)

	b, err := bencode.Marshal(pm)
	require.NoError(t, err)

	pexExtendedId := ExtensionNumber(7)
	msg := pm.Message(pexExtendedId)
	expected := []byte("\x00\x00\x00\x4c\x14\x07d5:added6:\x7f\x00\x00\x01\x55\xaa7:added.f1:\x116:added60:8:added6.f0:7:dropped0:8:dropped60:e")
	b, err = msg.MarshalBinary()
	require.NoError(t, err)
	require.EqualValues(t, b, expected)

	msg = Message{}
	dec := Decoder{
		R:         bufio.NewReader(bytes.NewBuffer(b)),
		MaxLength: 128,
	}
	pmOut := PexMsg{}
	err = dec.Decode(&msg)
	require.NoError(t, err)
	require.EqualValues(t, Extended, msg.Type)
	require.EqualValues(t, pexExtendedId, msg.ExtendedID)
	err = bencode.Unmarshal(msg.ExtendedPayload, &pmOut)
	require.NoError(t, err)
	require.EqualValues(t, len(pm.Added), len(pmOut.Added))
	require.EqualValues(t, pm.Added[0].IP, pmOut.Added[0].IP)
	require.EqualValues(t, pm.Added[0].Port, pmOut.Added[0].Port)
}
