package storage

import (
	"context"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

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
	qt.Assert(t, qt.IsNil(err))
	p := ts.PieceWithHash(info.Piece(0), g.None[[]byte]())
	qt.Check(t, qt.IsNil(p.MarkComplete()))
	// qt.Assert(t, qt.IsFalse(p.GetIsComplete()))
	n, err := p.ReadAt(make([]byte, 1), 0)
	qt.Check(t, qt.Not(qt.IsNil(err)))
	qt.Check(t, qt.Equals(n, int(0)))
	qt.Check(t, qt.IsFalse(p.Completion().Complete))
}

func TestMarkedCompleteMissingOnReadFile(t *testing.T) {
	testMarkedCompleteMissingOnRead(t, NewFile)
}

func TestMarkedCompleteMissingOnReadFileBoltDB(t *testing.T) {
	testMarkedCompleteMissingOnRead(t, NewBoltDB)
}
