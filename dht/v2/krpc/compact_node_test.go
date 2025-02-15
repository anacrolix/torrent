package krpc_test

import (
	"net/netip"
	"testing"

	"github.com/james-lawrence/torrent/dht/v2/krpc"
	"github.com/stretchr/testify/require"
)

func TestIPv4(t *testing.T) {
	addrs := krpc.CompactIPv4NodeAddrs{
		krpc.NewNodeAddrFromIPPort(netip.IPv4Unspecified().AsSlice(), 1),
		krpc.NewNodeAddrFromIPPort(netip.AddrFrom4([...]byte{127, 0, 0, 1}).AsSlice(), 1),
	}
	encoded, err := addrs.MarshalBencode()
	require.NoError(t, err)
	require.Equal(t, "12:\x00\x00\x00\x00\x00\x01\x7f\x00\x00\x01\x00\x01", string(encoded))
}

func TestIPv6(t *testing.T) {
	addrs := krpc.CompactIPv6NodeAddrs{
		krpc.NewNodeAddrFromIPPort(netip.IPv4Unspecified().AsSlice(), 1),
		krpc.NewNodeAddrFromIPPort(netip.IPv6LinkLocalAllNodes().AsSlice(), 1),
		krpc.NewNodeAddrFromIPPort(netip.IPv6LinkLocalAllRouters().AsSlice(), 1),
	}
	encoded, err := addrs.MarshalBencode()
	require.NoError(t, err)
	require.Equal(t, "54:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\xff\xff\x00\x00\x00\x00\x00\x01\xff\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x01\xff\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x01", string(encoded))
}
