package storage

import (
	"path/filepath"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
	"go.etcd.io/bbolt"

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

func TestBoltPieceCompletionDeleteTorrent(t *testing.T) {
	db, err := bbolt.Open(filepath.Join(t.TempDir(), "test.db"), 0o660, nil)
	qt.Assert(t, qt.IsNil(err))
	pc := &boltPieceCompletion{db}
	defer pc.Close()

	target := metainfo.HashBytes([]byte("target"))
	other := metainfo.HashBytes([]byte("other"))
	targetKey := metainfo.PieceKey{InfoHash: target, Index: 1}
	otherKey := metainfo.PieceKey{InfoHash: other, Index: 1}

	// Deleting with no completion bucket at all is a no-op.
	qt.Assert(t, qt.IsNil(pc.DeleteTorrent(target)))

	qt.Assert(t, qt.IsNil(pc.Set(targetKey, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Set(otherKey, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.DeleteTorrent(target)))

	c, err := pc.Get(targetKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(c.Ok))
	c, err = pc.Get(otherKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(c, Completion{Complete: true, Ok: true}))

	// Deleting a torrent with no stored state is idempotent.
	qt.Check(t, qt.IsNil(pc.DeleteTorrent(target)))
	qt.Check(t, qt.IsNil(pc.DeleteTorrent(metainfo.HashBytes([]byte("unknown")))))
}
