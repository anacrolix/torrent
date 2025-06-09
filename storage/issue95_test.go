package storage

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/metainfo"
)

// Two different torrents opened from the same storage. Closing one should not
// break the piece completion on the other.
func testIssue95(t *testing.T, c ClientImpl) {
	i1 := &metainfo.Info{
		Files:  []metainfo.FileInfo{{Path: []string{"a"}}},
		Pieces: make([]byte, 20),
	}
	t1, err := c.OpenTorrent(i1, metainfo.NewHashFromBytes([]byte("a")))
	require.NoError(t, err)
	i2 := &metainfo.Info{
		Files:  []metainfo.FileInfo{{Path: []string{"a"}}},
		Pieces: make([]byte, 20),
	}
	t2, err := c.OpenTorrent(i2, metainfo.NewHashFromBytes([]byte("b")))
	require.NoError(t, err)

	require.NoError(t, t1.Close())
	// TODO
	// t2p := t2.Piece(i2.Piece(0))
	// require.NotPanics(t, func() { t2p.Completion() })
	require.NoError(t, t2.Close())
}

func TestIssue95File(t *testing.T) {
	td, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(td)
	testIssue95(t, NewFile(td))
}

func TestIssue95MMap(t *testing.T) {
	td, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	defer os.RemoveAll(td)
	testIssue95(t, NewMMap(td))
}
