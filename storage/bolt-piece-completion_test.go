package storage

import (
	"testing"

	"github.com/go-quicktest/qt"
	"github.com/stretchr/testify/assert"

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
	assert.False(t, b.Ok)

	qt.Assert(t, qt.IsNil(pc.Set(pk, false)))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	assert.Equal(t, Completion{Complete: false, Ok: true}, b)

	qt.Assert(t, qt.IsNil(pc.Set(pk, true)))

	b, err = pc.Get(pk)
	qt.Assert(t, qt.IsNil(err))
	assert.Equal(t, Completion{Complete: true, Ok: true}, b)
}
