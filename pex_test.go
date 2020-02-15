package torrent

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPEXSnapshot(t *testing.T) {
	c1 := &connection{
		remoteAddr: IpPort{
			IP:   net.IP{0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0xff, 0xff, 0xac, 0x11, 0x0, 0x2},
			Port: 5,
		},
	}

	pex := newPex()
	pex.added(c1)
	tx := pex.snapshot()
	require.NotNil(t, tx)
	require.EqualValues(t, 1, len(tx.Added))
	if !c1.remoteAddr.IP.Equal(tx.Added[0].IP) {
		require.EqualValues(t, c1.remoteAddr.IP, tx.Added[0].IP)
	}
	require.EqualValues(t, c1.remoteAddr.Port, tx.Added[0].Port)
}
