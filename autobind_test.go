package torrent_test

import (
	"testing"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/james-lawrence/torrent/internal/x/bytesx"
	"github.com/stretchr/testify/require"
)

func TestSocketsBindSockets(t *testing.T) {
	s, err := torrent.Autosocket(t).Bind(torrent.NewClient(TestingSeedConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer s.Close()

	l, err := torrent.Autosocket(t).Bind(torrent.NewClient(TestingLeechConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer l.Close()

	testTransferRandomData(t, bytesx.KiB, s, l)
}
