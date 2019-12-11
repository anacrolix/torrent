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

func TestPexAppendAdded(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		addr := krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747}
		f := PexPrefersEncryption | PexOutgoingConn
		xm := PexMsg{}
		xm.AppendAdded(addr, f)
		require.EqualValues(t, len(xm.Added), 1)
		require.EqualValues(t, len(xm.AddedFlags), 1)
		require.EqualValues(t, len(xm.Added6), 0)
		require.EqualValues(t, len(xm.Added6Flags), 0)
		require.True(t, xm.Added[0].IP.Equal(addr.IP), "IPs should match")
		require.EqualValues(t, xm.Added[0].Port, addr.Port)
		require.EqualValues(t, xm.AddedFlags[0], f)
	})
	t.Run("ipv6", func(t *testing.T) {
		addr := krpc.NodeAddr{IP: net.IPv6loopback, Port: 4747}
		f := PexPrefersEncryption | PexOutgoingConn
		xm := PexMsg{}
		xm.AppendAdded(addr, f)
		require.EqualValues(t, len(xm.Added), 0)
		require.EqualValues(t, len(xm.AddedFlags), 0)
		require.EqualValues(t, len(xm.Added6), 1)
		require.EqualValues(t, len(xm.Added6Flags), 1)
		require.True(t, xm.Added6[0].IP.Equal(addr.IP), "IPs should match")
		require.EqualValues(t, xm.Added6[0].Port, addr.Port)
		require.EqualValues(t, xm.Added6Flags[0], f)
	})
	t.Run("unspecified", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		xm := PexMsg{}
		xm.AppendAdded(addr, 0)
		require.EqualValues(t, len(xm.Added), 0)
		require.EqualValues(t, len(xm.AddedFlags), 0)
		require.EqualValues(t, len(xm.Added6), 0)
		require.EqualValues(t, len(xm.Added6Flags), 0)
	})
}

func TestPexAppendDropped(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		addr := krpc.NodeAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4747}
		xm := PexMsg{}
		xm.AppendDropped(addr)
		require.EqualValues(t, len(xm.Dropped), 1)
		require.EqualValues(t, len(xm.Dropped6), 0)
		require.True(t, xm.Dropped[0].IP.Equal(addr.IP), "IPs should match")
		require.EqualValues(t, xm.Dropped[0].Port, addr.Port)
	})
	t.Run("ipv6", func(t *testing.T) {
		addr := krpc.NodeAddr{IP: net.IPv6loopback, Port: 4747}
		xm := PexMsg{}
		xm.AppendDropped(addr)
		require.EqualValues(t, len(xm.Dropped), 0)
		require.EqualValues(t, len(xm.Dropped6), 1)
		require.True(t, xm.Dropped6[0].IP.Equal(addr.IP), "IPs should match")
		require.EqualValues(t, xm.Dropped6[0].Port, addr.Port)
	})
	t.Run("unspecified", func(t *testing.T) {
		addr := krpc.NodeAddr{}
		xm := PexMsg{}
		xm.AppendDropped(addr)
		require.EqualValues(t, len(xm.Dropped), 0)
		require.EqualValues(t, len(xm.Dropped6), 0)
	})
}

func TestMarshalPexMessage(t *testing.T) {
	addr := krpc.NodeAddr{IP: net.IP{127, 0, 0, 1}, Port: 0x55aa}
	f := PexPrefersEncryption | PexOutgoingConn
	pm := PexMsg{}
	pm.AppendAdded(addr, f)

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
