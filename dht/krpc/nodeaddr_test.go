package krpc

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	IPv4    = net.IPv4
	ParseIP = net.ParseIP
)

func TestUnmarshalNodeAddr(t *testing.T) {
	var na NodeAddr
	require.NoError(t, na.UnmarshalBinary([]byte("\x01\x02\x03\x04\x05\x06")))
	assert.EqualValues(t, "1.2.3.4", na.IP().String())
}

var naEqualTests = []struct {
	a, b NodeAddr
	out  bool
}{
	{NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 11), NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 11), true},
	{NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 11), NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 22), false},
	{NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 11), NewNodeAddrFromIPPort(IPv4(192, 168, 0, 3), 11), false},
	{NewNodeAddrFromIPPort(IPv4(172, 16, 1, 1), 11), NewNodeAddrFromIPPort(IPv4(192, 168, 0, 3), 22), false},
	{NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 11), NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 11), true},
	{NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 11), NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 22), false},
	{NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 11), NewNodeAddrFromIPPort(ParseIP("fe80::420b"), 11), false},
	{NewNodeAddrFromIPPort(ParseIP("2001:db8:1:2::1"), 11), NewNodeAddrFromIPPort(ParseIP("fe80::420b"), 22), false},
}

func TestNodeAddrEqual(t *testing.T) {
	for _, tc := range naEqualTests {
		out := tc.a.Compare(tc.b) == 0
		if out != tc.out {
			t.Errorf("NodeAddr(%v).Equal(%v) = %v, want %v", tc.a, tc.b, out, tc.out)
		}
	}
}
