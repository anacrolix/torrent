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
	assert.EqualValues(t, "1.2.3.4", na.IP.String())
}

var naEqualTests = []struct {
	a, b NodeAddr
	out  bool
}{
	{NodeAddr{IPv4(172, 16, 1, 1), 11}, NodeAddr{IPv4(172, 16, 1, 1), 11}, true},
	{NodeAddr{IPv4(172, 16, 1, 1), 11}, NodeAddr{IPv4(172, 16, 1, 1), 22}, false},
	{NodeAddr{IPv4(172, 16, 1, 1), 11}, NodeAddr{IPv4(192, 168, 0, 3), 11}, false},
	{NodeAddr{IPv4(172, 16, 1, 1), 11}, NodeAddr{IPv4(192, 168, 0, 3), 22}, false},
	{NodeAddr{ParseIP("2001:db8:1:2::1"), 11}, NodeAddr{ParseIP("2001:db8:1:2::1"), 11}, true},
	{NodeAddr{ParseIP("2001:db8:1:2::1"), 11}, NodeAddr{ParseIP("2001:db8:1:2::1"), 22}, false},
	{NodeAddr{ParseIP("2001:db8:1:2::1"), 11}, NodeAddr{ParseIP("fe80::420b"), 11}, false},
	{NodeAddr{ParseIP("2001:db8:1:2::1"), 11}, NodeAddr{ParseIP("fe80::420b"), 22}, false},
}

func TestNodeAddrEqual(t *testing.T) {
	for _, tc := range naEqualTests {
		out := tc.a.Equal(tc.b)
		if out != tc.out {
			t.Errorf("NodeAddr(%v).Equal(%v) = %v, want %v", tc.a, tc.b, out, tc.out)
		}
	}
}
