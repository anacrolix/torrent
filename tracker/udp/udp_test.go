package udp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/require"
)

// Ensure net.IPs are stored big-endian, to match the way they're read from
// the wire.
func TestNetIPv4Bytes(t *testing.T) {
	ip := net.IP([]byte{127, 0, 0, 1})
	if ip.String() != "127.0.0.1" {
		t.FailNow()
	}
	if string(ip) != "\x7f\x00\x00\x01" {
		t.Fatal([]byte(ip))
	}
}

func TestMarshalAnnounceResponse(t *testing.T) {
	peers := krpc.CompactIPv4NodeAddrs{
		{[]byte{127, 0, 0, 1}, 2},
		{[]byte{255, 0, 0, 3}, 4},
	}
	b, err := peers.MarshalBinary()
	require.NoError(t, err)
	require.EqualValues(t,
		"\x7f\x00\x00\x01\x00\x02\xff\x00\x00\x03\x00\x04",
		b)
	require.EqualValues(t, 12, binary.Size(AnnounceResponseHeader{}))
}

// Failure to write an entire packet to UDP is expected to given an error.
func TestLongWriteUDP(t *testing.T) {
	t.Parallel()
	l, err := net.ListenUDP("udp4", nil)
	require.NoError(t, err)
	defer l.Close()
	c, err := net.DialUDP("udp", nil, l.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for msgLen := 1; ; msgLen *= 2 {
		n, err := c.Write(make([]byte, msgLen))
		if err != nil {
			require.Contains(t, err.Error(), "message too long")
			return
		}
		if n < msgLen {
			t.FailNow()
		}
	}
}

func TestShortBinaryRead(t *testing.T) {
	var data ResponseHeader
	err := binary.Read(bytes.NewBufferString("\x00\x00\x00\x01"), binary.BigEndian, &data)
	if err != io.ErrUnexpectedEOF {
		t.FailNow()
	}
}

func TestConvertInt16ToInt(t *testing.T) {
	i := 50000
	if int(uint16(int16(i))) != 50000 {
		t.FailNow()
	}
}

func TestConnClientLogDispatchUnknownTransactionId(t *testing.T) {
	const network = "udp"
	cc, err := NewConnClient(NewConnClientOpts{
		Network: network,
	})
	c := qt.New(t)
	c.Assert(err, qt.IsNil)
	defer cc.Close()
	pc, err := net.ListenPacket(network, ":0")
	c.Assert(err, qt.IsNil)
	defer pc.Close()
	ccAddr := *cc.LocalAddr().(*net.UDPAddr)
	ccAddr.IP = net.IPv6loopback
	_, err = pc.WriteTo(make([]byte, 30), &ccAddr)
	c.Assert(err, qt.IsNil)
}
