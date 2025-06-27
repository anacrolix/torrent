package torrent_test

import (
	"testing"

	"github.com/james-lawrence/torrent"
	"github.com/james-lawrence/torrent/dht/int160"
	"github.com/james-lawrence/torrent/internal/bytesx"
	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

func TestMetadata(t *testing.T) {
	t.Run("New should create torrent metadata from just an infohash", func(t *testing.T) {
		md, err := torrent.New(int160.Random().AsByteArray())
		require.NoError(t, err)

		require.Nil(t, md.InfoBytes) // info bytes should be missing
		require.Len(t, md.Webseeds, 0)
		require.Len(t, md.Trackers, 0)
		require.Len(t, md.DHTNodes, 0)
		require.EqualValues(t, 16*bytesx.KiB, md.ChunkSize)
	})

	t.Run("NewFromMetaInfoFile", func(t *testing.T) {
		md, err := torrent.NewFromMetaInfoFile("testdata/archlinux-2025.06.01-x86_64.iso.torrent")
		require.NoError(t, err)

		require.Equal(t, errorsx.Must(int160.FromHexEncodedString("a492f8b92a25b0399c87715fc228c864ac5a7bfb")), int160.New(md.InfoBytes))
		require.Equal(t, "archlinux-2025.06.01-x86_64.iso", md.DisplayName)
		require.Len(t, md.Webseeds, 406)
		require.Len(t, md.Trackers, 0)
		require.Len(t, md.DHTNodes, 0)
		require.EqualValues(t, 16*bytesx.KiB, md.ChunkSize)
	})

	t.Run("NewFromMetaInfo", func(t *testing.T) {
		minfo, err := metainfo.LoadFromFile("testdata/archlinux-2025.06.01-x86_64.iso.torrent")
		require.NoError(t, err)

		md, err := torrent.NewFromMetaInfo(minfo)
		require.NoError(t, err)

		require.Equal(t, errorsx.Must(int160.FromHexEncodedString("a492f8b92a25b0399c87715fc228c864ac5a7bfb")), int160.New(md.InfoBytes))
		require.Equal(t, "archlinux-2025.06.01-x86_64.iso", md.DisplayName)
		require.Len(t, md.Webseeds, 406)
		require.Len(t, md.Trackers, 0)
		require.Len(t, md.DHTNodes, 0)
		require.EqualValues(t, 16*bytesx.KiB, md.ChunkSize)
	})
}
