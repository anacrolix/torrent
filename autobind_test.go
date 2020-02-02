package torrent

import (
	"testing"

	"github.com/anacrolix/torrent/internal/x/bytesx"
	"github.com/anacrolix/utp"
	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"
)

var _ = spew.Sdump

func TestAutobindSockets(t *testing.T) {
	autosocket := func() socket {
		s, err := utp.NewSocket("udp", "localhost:0")
		require.NoError(t, err)
		return utpSocketSocket{utpSocket: s, network: "udp", d: nil}
	}

	s, err := NewSocketsBind(autosocket()).Bind(NewClient(TestingSeedConfig(autotempdir(t))))
	require.NoError(t, err)
	defer s.Close()

	l, err := NewSocketsBind(autosocket()).Bind(NewClient(TestingLeechConfig(autotempdir(t))))
	require.NoError(t, err)
	defer l.Close()
	testTransferRandomData(t, bytesx.KiB, s, l)
}
