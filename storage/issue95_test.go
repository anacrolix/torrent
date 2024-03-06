package storage

import (
	"testing"

	"github.com/anacrolix/missinggo/v2/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
)

// Two different torrents opened from the same storage. Closing one should not
// break the piece completion on the other.
func testIssue95(t *testing.T, ci ClientImpl) {
	i1 := &metainfo.Info{
		Files:  []metainfo.FileInfo{{Path: []string{"a"}}},
		Pieces: make([]byte, 20),
	}
	c := NewClient(ci)
	t1, err := c.OpenTorrent(i1, metainfo.HashBytes([]byte("a")))
	require.NoError(t, err)
	defer t1.Close()
	i2 := &metainfo.Info{
		Files:  []metainfo.FileInfo{{Path: []string{"a"}}},
		Pieces: make([]byte, 20),
	}
	t2, err := c.OpenTorrent(i2, metainfo.HashBytes([]byte("b")))
	require.NoError(t, err)
	defer t2.Close()
	t2p := t2.Piece(i2.Piece(0))
	assert.NoError(t, t1.Close())
	assert.NotPanics(t, func() { t2p.Completion() })
}

func TestIssue95File(t *testing.T) {
	td := t.TempDir()
	cs := NewFile(td)
	defer cs.Close()
	testIssue95(t, cs)
}

func TestIssue95MMap(t *testing.T) {
	td := t.TempDir()
	cs := NewMMap(td)
	defer cs.Close()
	testIssue95(t, cs)
}

func TestIssue95ResourcePieces(t *testing.T) {
	testIssue95(t, NewResourcePieces(resource.OSFileProvider{}))
}
