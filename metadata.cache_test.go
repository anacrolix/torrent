package torrent_test

import (
	"testing"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

func TestMetadataCacheShouldPersistAndLoadCorrectly(t *testing.T) {
	md, err := torrent.NewFromMetaInfoFile("testdata/debian-9.1.0-amd64-netinst.iso.torrent")
	require.NoError(t, err)

	cache := torrent.NewMetadataCache(t.TempDir())
	require.NoError(t, cache.Write(md))
	md1, err := cache.Read(int160.FromBytes(md.ID.Bytes()))
	require.NoError(t, err)

	require.Equal(t, md.InfoBytes, md1.InfoBytes)
	require.Equal(t, md.DisplayName, md1.DisplayName)
	require.Equal(t, md.ID, metainfo.NewHashFromBytes(md1.InfoBytes))
	require.Equal(t, md.ID, metainfo.NewHashFromBytes(md.InfoBytes))
}
