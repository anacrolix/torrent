package storage

import (
	"context"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestMmapWindows(t *testing.T) {
	dir, mi := testutil.GreetingTestTorrent()
	cs := NewMMap(dir)
	defer func() {
		qt.Check(t, qt.IsNil(cs.Close()))
	}()
	info, err := mi.UnmarshalInfo()
	qt.Assert(t, qt.IsNil(err))
	ts, err := cs.OpenTorrent(context.Background(), &info, mi.HashInfoBytes())
	qt.Assert(t, qt.IsNil(err))
	defer func() {
		qt.Check(t, qt.IsNil(ts.Close()))
	}()
}
