package krpc

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalSlice(t *testing.T) {
	var data CompactIPv4NodeInfo
	err := data.UnmarshalBencode([]byte("52:" +
		"\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x02\x03\x04\x05\x06" +
		"\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x03\x04\x05\x06\x07"))
	require.NoError(t, err)
	require.Len(t, data, 2)
	assert.Equal(t, "1.2.3.4", data[0].Addr.IP.String())
	assert.Equal(t, "2.3.4.5", data[1].Addr.IP.String())
}

var nodeAddrIndexTests4 = []struct {
	v   CompactIPv4NodeAddrs
	a   NodeAddr
	out int
}{
	{[]NodeAddr{{IPv4(172, 16, 1, 1), 11}, {IPv4(192, 168, 0, 3), 11}}, NodeAddr{IPv4(172, 16, 1, 1), 11}, 0},
	{[]NodeAddr{{IPv4(172, 16, 1, 1), 11}, {IPv4(192, 168, 0, 3), 11}}, NodeAddr{IPv4(192, 168, 0, 3), 11}, 1},
	{[]NodeAddr{{IPv4(172, 16, 1, 1), 11}, {IPv4(192, 168, 0, 3), 11}}, NodeAddr{IPv4(127, 0, 0, 1), 11}, -1},
	{[]NodeAddr{}, NodeAddr{IPv4(127, 0, 0, 1), 11}, -1},
	{[]NodeAddr{}, NodeAddr{}, -1},
}

func TestNodeAddrIndex4(t *testing.T) {
	for _, tc := range nodeAddrIndexTests4 {
		out := tc.v.Index(tc.a)
		if out != tc.out {
			t.Errorf("CompactIPv4NodeAddrs(%v).Index(%v) = %v, want %v", tc.v, tc.a, out, tc.out)
		}
	}
}

var nodeAddrIndexTests6 = []struct {
	v   CompactIPv6NodeAddrs
	a   NodeAddr
	out int
}{
	{[]NodeAddr{{ParseIP("2001::1"), 11}, {ParseIP("4004::1"), 11}}, NodeAddr{ParseIP("2001::1"), 11}, 0},
	{[]NodeAddr{{ParseIP("2001::1"), 11}, {ParseIP("4004::1"), 11}}, NodeAddr{ParseIP("4004::1"), 11}, 1},
	{[]NodeAddr{{ParseIP("2001::1"), 11}, {ParseIP("4004::1"), 11}}, NodeAddr{ParseIP("::1"), 11}, -1},
	{[]NodeAddr{}, NodeAddr{ParseIP("::1"), 11}, -1},
	{[]NodeAddr{}, NodeAddr{}, -1},
}

func TestNodeAddrIndex6(t *testing.T) {
	for _, tc := range nodeAddrIndexTests6 {
		out := tc.v.Index(tc.a)
		if out != tc.out {
			t.Errorf("CompactIPv6NodeAddrs(%v).Index(%v) = %v, want %v", tc.v, tc.a, out, tc.out)
		}
	}
}

var marshalIPv4SliceTests = []struct {
	in     CompactIPv4NodeAddrs
	out    []byte
	panics bool
}{
	{[]NodeAddr{{net.IP{172, 16, 1, 1}, 3}}, []byte{172, 16, 1, 1, 0, 3}, false},
	{[]NodeAddr{{net.IPv4(172, 16, 1, 1), 4}}, []byte{172, 16, 1, 1, 0, 4}, false},
	{[]NodeAddr{{net.IPv4(172, 16, 1, 1), 5}, {net.IPv4(192, 168, 0, 3), 6}}, []byte{
		172, 16, 1, 1, 0, 5,
		192, 168, 0, 3, 0, 6,
	}, false},
	{[]NodeAddr{{ParseIP("2001::1"), 7}}, nil, true},
	{[]NodeAddr{{nil, 8}}, nil, true},
}

func TestMarshalCompactIPv4NodeAddrs(t *testing.T) {
	for _, tc := range marshalIPv4SliceTests {
		runFunc := assert.NotPanics
		if tc.panics {
			runFunc = assert.Panics
		}
		runFunc(t, func() {
			out, err := tc.in.MarshalBinary()
			require.NoError(t, err)
			assert.Equal(t, tc.out, out, "for input %v, %v", tc.in)
		})
	}
}
