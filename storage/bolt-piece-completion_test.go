package storage

import (
	"testing"

	g "github.com/anacrolix/generics"
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

	qt.Check(t, qt.IsNil(pc.Set(pk, g.Some(false))))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(b, Completion{Complete: false, Ok: true}))

	qt.Check(t, qt.IsNil(pc.Set(pk, g.Some(true))))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(b, Completion{Complete: true, Ok: true}))

	// Setting None forgets the state rather than recording not-complete.
	qt.Check(t, qt.IsNil(pc.Set(pk, g.None[bool]())))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(b, Completion{}))

	// Setting unknown when it's already unknown is a no-op, not an error.
	qt.Check(t, qt.IsNil(pc.Set(pk, g.None[bool]())))
}
