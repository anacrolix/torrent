package storage

import (
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

func TestBoltPieceCompletion(t *testing.T) {
	td := t.TempDir()

	pc, err := NewBoltPieceCompletion(td)
	qt.Assert(t, qt.IsNil(err))
	defer pc.Close()

	pk := metainfo.PieceKey{}

	b, err := pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(b.Ok))

	qt.Check(t, qt.IsNil(pc.Set(pk, false)))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(b, Completion{Complete: false, Ok: true}))

	qt.Check(t, qt.IsNil(pc.Set(pk, true)))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(b, Completion{Complete: true, Ok: true}))
}
