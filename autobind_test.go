package torrent_test

import (
	"testing"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSocketsBindSockets(t *testing.T) {
	dir := t.TempDir()
	s, err := torrent.Autosocket(t).Bind(torrent.NewClient(TestingSeedConfig(t, dir)))
	require.NoError(t, err)
	defer s.Close()

	l, err := torrent.Autosocket(t).Bind(torrent.NewClient(TestingLeechConfig(t, testutil.Autodir(t))))
	require.NoError(t, err)
	defer l.Close()

	testTransferRandomData(t, dir, bytesx.KiB, s, l)
}
