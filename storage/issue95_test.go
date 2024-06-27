package storage

import (
	"context"
	"testing"

	"github.com/anacrolix/missinggo/v2/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
)

// Two different torrents opened from the same storage. Closing one should not
// break the piece completion on the other.
func testIssue95(t *testing.T, ci ClientImpl) {
	info := metainfo.Info{
		Files:       []metainfo.FileInfo{{Path: []string{"a"}, Length: 1}},
		Pieces:      make([]byte, 20),
		PieceLength: 1,
	}
	c := NewClient(ci)
	t1, err := c.OpenTorrent(context.Background(), &info, metainfo.HashBytes([]byte("a")))
	require.NoError(t, err)
	defer t1.Close()
	t2, err := c.OpenTorrent(context.Background(), &info, metainfo.HashBytes([]byte("b")))
	require.NoError(t, err)
	defer t2.Close()
	t2p := t2.Piece(info.Piece(0))
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
