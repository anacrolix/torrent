package storage

import (
	"context"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/metainfo"
)

func testMarkedCompleteMissingOnRead(t *testing.T, csf func(string) ClientImplCloser) {
	td := t.TempDir()
	cic := csf(td)
	defer cic.Close()
	cs := NewClient(cic)
	info := &metainfo.Info{
		PieceLength: 1,
		Files:       []metainfo.FileInfo{{Path: []string{"a"}, Length: 1}},
		Pieces:      make([]byte, 20),
	}
	ts, err := cs.OpenTorrent(context.Background(), info, metainfo.Hash{})
	require.NoError(t, err)
	p := ts.PieceWithHash(info.Piece(0), g.None[[]byte]())
	require.NoError(t, p.MarkComplete())
	// require.False(t, p.GetIsComplete())
	n, err := p.ReadAt(make([]byte, 1), 0)
	require.Error(t, err)
	require.EqualValues(t, 0, n)
	require.False(t, p.Completion().Complete)
}

func TestMarkedCompleteMissingOnReadFile(t *testing.T) {
	testMarkedCompleteMissingOnRead(t, NewFile)
}

func TestMarkedCompleteMissingOnReadFileBoltDB(t *testing.T) {
	testMarkedCompleteMissingOnRead(t, NewBoltDB)
}
